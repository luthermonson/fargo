// Package reconcile holds the mode-independent engine that drives a rollout
// through the kine transaction lifecycle: begin when a rollout starts, wait
// for health, commit when healthy, roll back on failure or deadline. It is
// idempotent against the ledger (GET /txn/{id} before begin) so a controller
// restart never double-begins or rolls back a committed transaction.
package reconcile

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/luthermonson/fargo/pkg/txn"
)

// Outcome is the annotation/label value recorded against a watched object.
type Outcome string

const (
	OutcomeOpen       Outcome = "open"
	OutcomeCommitted  Outcome = "committed"
	OutcomeRolledBack Outcome = "rolledBack"
)

// ObjRef locates the watched object so the engine can write an outcome back.
// The GVR fields identify the resource (filled by the mode) so an annotator
// can address the object without a second lookup.
type ObjRef struct {
	Namespace   string
	Name        string
	GVRGroup    string
	GVRVersion  string
	GVRResource string
}

// Signal is a rollout event mapped from a deployment system's object. Modes
// produce Signals; the engine turns them into kine calls.
type Signal struct {
	Kind   SignalKind
	ID     string
	Ref    ObjRef
	Scope  txn.Scope
	Reason string
}

// SignalKind enumerates the rollout events an engine reacts to.
type SignalKind int

const (
	// SignalNone means the object carries no actionable rollout state.
	SignalNone SignalKind = iota
	// SignalStarted means a rollout began; open a transaction.
	SignalStarted
	// SignalSucceeded means the rollout is healthy; commit.
	SignalSucceeded
	// SignalFailed means the rollout failed; roll back.
	SignalFailed
	// SignalInProgress means the rollout is applied but not yet healthy. It
	// carries no action of its own; the engine uses it to re-adopt an open
	// transaction (and re-arm its health deadline) after a controller restart,
	// and to write back an outcome the ledger already holds.
	SignalInProgress
)

// TxnClient is the subset of the kine control client the engine needs.
type TxnClient interface {
	Begin(ctx context.Context, id string, scope txn.Scope) (*txn.Transaction, error)
	Get(ctx context.Context, id string) (*txn.Transaction, error)
	Commit(ctx context.Context, id string) (*txn.Transaction, error)
	Rollback(ctx context.Context, id string) (*txn.RollbackResult, error)
}

// Annotator records a transaction outcome back onto the watched object. For
// argo/flux this patches annotations; for crd it updates status. A nil
// Annotator (or dry-run) makes the engine skip write-back.
type Annotator interface {
	Record(ctx context.Context, ref ObjRef, id string, outcome Outcome) error
}

// Observer receives counts of engine actions, for metrics. All methods may be
// called concurrently; a nil Observer is ignored.
type Observer interface {
	Begun(id string)
	Committed(id string)
	RolledBack(id string)
	Errored(id string)
}

// Config configures an Engine.
type Config struct {
	Client         TxnClient
	Annotator      Annotator
	Observer       Observer
	HealthDeadline time.Duration
	DryRun         bool
	Logger         *slog.Logger
	// now and afterFunc are injectable for tests; nil uses the real clock.
	now       func() time.Time
	afterFunc func(time.Duration, func()) stopper
	// deadlineRetries and retryBackoff bound the deadline rollback's retry
	// loop; zero values take the defaults (60 attempts from 2 s, capped at 2 m).
	deadlineRetries int
	retryBackoff    time.Duration
}

type stopper interface{ Stop() bool }

// Engine drives transactions. It is safe for concurrent use.
type Engine struct {
	cfg    Config
	log    *slog.Logger
	mu     sync.Mutex
	active map[string]*track // id -> in-flight transaction
	// recorded remembers the outcomes written back for ids that are no longer
	// tracked, so a restarted controller patches a finished transaction's
	// outcome once rather than on every object event.
	recorded map[string]Outcome
}

type track struct {
	ref      ObjRef
	scope    txn.Scope
	deadline stopper
	done     bool // committed or rolled back locally
}

// NewEngine builds an Engine.
func NewEngine(cfg Config) *Engine {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HealthDeadline <= 0 {
		cfg.HealthDeadline = 5 * time.Minute
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.afterFunc == nil {
		cfg.afterFunc = func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }
	}
	if cfg.deadlineRetries <= 0 {
		cfg.deadlineRetries = 60
	}
	if cfg.retryBackoff <= 0 {
		cfg.retryBackoff = 2 * time.Second
	}
	return &Engine{cfg: cfg, log: cfg.Logger, active: map[string]*track{}, recorded: map[string]Outcome{}}
}

// Handle drives one Signal to its kine effect. It is the single entry point
// every mode funnels events through.
func (e *Engine) Handle(ctx context.Context, s Signal) error {
	switch s.Kind {
	case SignalStarted:
		return e.begin(ctx, s)
	case SignalSucceeded:
		return e.commit(ctx, s)
	case SignalFailed:
		return e.rollback(ctx, s, s.Reason)
	case SignalInProgress:
		return e.adopt(ctx, s)
	default:
		return nil
	}
}

// adopt handles an in-progress rollout the engine is not tracking: after a
// restart the Running phase that would have begun the transaction is gone, so
// the open transaction in the ledger is re-adopted here and its health
// deadline re-armed (from now; the original start is not recoverable). A
// transaction the ledger already closed gets its outcome written back once.
func (e *Engine) adopt(ctx context.Context, s Signal) error {
	if s.ID == "" {
		return nil
	}
	e.mu.Lock()
	_, tracked := e.active[s.ID]
	_, done := e.recorded[s.ID]
	e.mu.Unlock()
	if tracked || done {
		return nil
	}
	cur, err := e.cfg.Client.Get(ctx, s.ID)
	if err != nil {
		if txn.IsNotFound(err) {
			return nil
		}
		return err
	}
	switch cur.State {
	case txn.StateOpen:
		e.log.Info("adopting open transaction for an in-progress rollout; re-arming the health deadline", "id", s.ID, "baseRev", cur.BaseRev)
		e.register(s.ID, s.Ref, s.Scope)
	case txn.StateCommitted:
		e.recordOnce(ctx, s.Ref, s.ID, OutcomeCommitted)
	case txn.StateRolledBack:
		e.recordOnce(ctx, s.Ref, s.ID, OutcomeRolledBack)
	}
	return nil
}

// recordOnce writes a finished outcome back for an untracked id exactly once.
func (e *Engine) recordOnce(ctx context.Context, ref ObjRef, id string, outcome Outcome) {
	e.mu.Lock()
	if _, ok := e.recorded[id]; ok {
		e.mu.Unlock()
		return
	}
	if len(e.recorded) > 4096 {
		e.recorded = map[string]Outcome{}
	}
	e.recorded[id] = outcome
	e.mu.Unlock()
	e.record(ctx, ref, id, outcome)
}

func (e *Engine) begin(ctx context.Context, s Signal) error {
	if s.ID == "" {
		return nil
	}
	e.mu.Lock()
	if _, ok := e.active[s.ID]; ok {
		e.mu.Unlock()
		return nil // already tracking; never begin twice for one rollout id
	}
	e.mu.Unlock()

	// Idempotency via the ledger: a restart must not double-begin, and must
	// pick up a transaction opened by a previous controller instance.
	existing, err := e.cfg.Client.Get(ctx, s.ID)
	if err != nil && !txn.IsNotFound(err) {
		return err
	}
	if existing != nil && err == nil {
		switch existing.State {
		case txn.StateCommitted, txn.StateRolledBack:
			e.log.Info("transaction already finished; skipping begin", "id", s.ID, "state", existing.State)
			return nil
		case txn.StateOpen:
			e.log.Info("adopting open transaction after restart", "id", s.ID, "baseRev", existing.BaseRev)
			e.register(s.ID, s.Ref, s.Scope)
			e.record(ctx, s.Ref, s.ID, OutcomeOpen)
			return nil
		}
	}

	if e.cfg.DryRun {
		e.log.Info("dry-run: would begin transaction", "id", s.ID, "scope", scopeSummary(s.Scope))
		e.register(s.ID, s.Ref, s.Scope)
		return nil
	}

	t, err := e.cfg.Client.Begin(ctx, s.ID, s.Scope)
	if err != nil {
		if txn.IsConflict(err) {
			e.log.Warn("begin conflicts with an open transaction; scope overlap", "id", s.ID, "err", err)
		}
		return err
	}
	e.log.Info("began transaction", "id", t.ID, "baseRev", t.BaseRev, "baseCommit", t.BaseCommit, "scope", scopeSummary(s.Scope))
	e.register(s.ID, s.Ref, s.Scope)
	e.record(ctx, s.Ref, s.ID, OutcomeOpen)
	e.observe(func(o Observer) { o.Begun(t.ID) })
	return nil
}

func (e *Engine) observe(f func(Observer)) {
	if e.cfg.Observer != nil {
		f(e.cfg.Observer)
	}
}

// register starts tracking an open transaction and arms its health deadline.
func (e *Engine) register(id string, ref ObjRef, scope txn.Scope) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.active[id]; ok {
		return
	}
	tr := &track{ref: ref, scope: scope}
	tr.deadline = e.cfg.afterFunc(e.cfg.HealthDeadline, func() {
		// A fresh context: the informer context may be mid-shutdown, but a
		// deadline rollback must still try to run. Unlike event-driven
		// rollbacks, nothing re-triggers this one, so keep trying with backoff
		// until kine answers; an open transaction left behind blocks every
		// later rollout on the same scope with a 409.
		backoff := e.cfg.retryBackoff
		for attempt := 1; ; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := e.rollback(ctx, Signal{Kind: SignalFailed, ID: id, Ref: ref}, "health deadline exceeded")
			cancel()
			if err == nil {
				return
			}
			e.log.Error("deadline rollback failed; retrying", "id", id, "attempt", attempt, "err", err)
			if attempt >= e.cfg.deadlineRetries {
				e.log.Error("deadline rollback gave up; the transaction stays open in the ledger", "id", id)
				return
			}
			time.Sleep(backoff)
			if backoff < 2*time.Minute {
				backoff *= 2
			}
		}
	})
	e.active[id] = tr
}

func (e *Engine) commit(ctx context.Context, s Signal) error {
	id := s.ID
	if id == "" {
		return nil
	}
	e.mu.Lock()
	tr := e.active[id]
	_, recorded := e.recorded[id]
	if (tr != nil && tr.done) || recorded {
		e.mu.Unlock()
		return nil // finished and written back; every later event is noise
	}
	e.mu.Unlock()
	ref := e.refOr(id, s.Ref)

	// Confirm state in the ledger before acting.
	cur, err := e.cfg.Client.Get(ctx, id)
	if err != nil {
		if txn.IsNotFound(err) {
			return nil // nothing was begun
		}
		return err
	}
	switch cur.State {
	case txn.StateCommitted:
		// Already committed (by a previous instance, or a retry after the
		// annotation write failed): make sure the outcome is written back.
		e.finish(id)
		e.recordOnce(ctx, ref, id, OutcomeCommitted)
		return nil
	case txn.StateRolledBack:
		e.log.Warn("cannot commit a rolled-back transaction", "id", id)
		e.finish(id)
		e.recordOnce(ctx, ref, id, OutcomeRolledBack)
		return nil
	}

	if e.cfg.DryRun {
		e.log.Info("dry-run: would commit transaction", "id", id)
		e.record(ctx, ref, id, OutcomeCommitted)
		e.finish(id)
		return nil
	}

	t, err := e.cfg.Client.Commit(ctx, id)
	if err != nil {
		return err
	}
	e.log.Info("committed transaction", "id", t.ID)
	e.finish(id)
	e.recordOnce(ctx, ref, id, OutcomeCommitted)
	e.observe(func(o Observer) { o.Committed(t.ID) })
	return nil
}

func (e *Engine) rollback(ctx context.Context, s Signal, reason string) error {
	id := s.ID
	if id == "" {
		return nil
	}
	e.mu.Lock()
	tr := e.active[id]
	_, recorded := e.recorded[id]
	if (tr != nil && tr.done) || recorded {
		e.mu.Unlock()
		return nil // finished and written back; every later event is noise
	}
	e.mu.Unlock()
	ref := e.refOr(id, s.Ref)

	cur, err := e.cfg.Client.Get(ctx, id)
	if err != nil {
		if txn.IsNotFound(err) {
			return nil
		}
		return err
	}
	switch cur.State {
	case txn.StateCommitted:
		// never roll back a committed transaction
		e.log.Warn("refusing to roll back a committed transaction", "id", id, "reason", reason)
		e.finish(id)
		e.recordOnce(ctx, ref, id, OutcomeCommitted)
		return nil
	case txn.StateRolledBack:
		e.finish(id)
		e.recordOnce(ctx, ref, id, OutcomeRolledBack)
		return nil
	}

	if e.cfg.DryRun {
		e.log.Info("dry-run: would roll back transaction", "id", id, "reason", reason)
		e.record(ctx, ref, id, OutcomeRolledBack)
		e.finish(id)
		return nil
	}

	res, err := e.cfg.Client.Rollback(ctx, id)
	if err != nil {
		return err
	}
	e.log.Info("rolled back transaction", "id", res.ID, "reason", reason, "events", res.Events, "freezeMs", res.FreezeMs, "reverted", len(res.Reverted))
	e.finish(id)
	e.recordOnce(ctx, ref, id, OutcomeRolledBack)
	e.observe(func(o Observer) { o.RolledBack(res.ID) })
	return nil
}

// finish marks a tracked transaction done and cancels its deadline timer.
func (e *Engine) finish(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tr, ok := e.active[id]; ok {
		if tr.deadline != nil {
			tr.deadline.Stop()
		}
		tr.done = true
		delete(e.active, id)
	}
}

func (e *Engine) refOf(id string) ObjRef {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tr, ok := e.active[id]; ok {
		return tr.ref
	}
	return ObjRef{}
}

// refOr returns the tracked ref for id, or the signal's own ref when the
// engine is not tracking it (after a restart).
func (e *Engine) refOr(id string, fallback ObjRef) ObjRef {
	if r := e.refOf(id); r.Name != "" {
		return r
	}
	return fallback
}

func (e *Engine) record(ctx context.Context, ref ObjRef, id string, outcome Outcome) {
	if e.cfg.Annotator == nil || e.cfg.DryRun {
		return
	}
	if ref.Name == "" {
		return
	}
	if err := e.cfg.Annotator.Record(ctx, ref, id, outcome); err != nil {
		e.log.Warn("failed to record outcome on object", "id", id, "outcome", outcome, "err", err)
	}
}

// Active returns the ids currently being tracked (for metrics/tests).
func (e *Engine) Active() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.active))
	for id := range e.active {
		out = append(out, id)
	}
	return out
}

func scopeSummary(s txn.Scope) string {
	return slogScope(s)
}
