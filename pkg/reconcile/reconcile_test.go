package reconcile

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/luthermonson/fargo/pkg/txn"
	"github.com/luthermonson/fargo/pkg/txntest"
)

// fakeTimer is an injectable afterFunc that captures the fired callback so a
// test can trigger the health deadline deterministically.
type fakeTimer struct {
	mu      sync.Mutex
	fire    func()
	stopped bool
}

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	return true
}

type countingObserver struct {
	mu                           sync.Mutex
	begun, committed, rolledBack int
}

func (o *countingObserver) Begun(string)      { o.mu.Lock(); o.begun++; o.mu.Unlock() }
func (o *countingObserver) Committed(string)  { o.mu.Lock(); o.committed++; o.mu.Unlock() }
func (o *countingObserver) RolledBack(string) { o.mu.Lock(); o.rolledBack++; o.mu.Unlock() }
func (o *countingObserver) Errored(string)    {}

func newTestEngine(t *testing.T) (*Engine, *txntest.FakeKine, *countingObserver, **fakeTimer) {
	t.Helper()
	fk := txntest.New("")
	srv := fk.Server()
	t.Cleanup(srv.Close)
	client, err := txn.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	obs := &countingObserver{}
	var lastTimer *fakeTimer
	cfg := Config{
		Client:         client,
		Observer:       obs,
		HealthDeadline: time.Hour,
		afterFunc: func(_ time.Duration, f func()) stopper {
			ft := &fakeTimer{fire: f}
			lastTimer = ft
			return ft
		},
	}
	return NewEngine(cfg), fk, obs, &lastTimer
}

func started(id string) Signal {
	return Signal{Kind: SignalStarted, ID: id, Ref: ObjRef{Namespace: "apps", Name: "podinfo"},
		Scope: txn.Scope{Keys: []string{"apps/deployments/apps/podinfo"}}}
}

func TestBeginOnlyOnce(t *testing.T) {
	e, fk, obs, _ := newTestEngine(t)
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"

	for i := 0; i < 3; i++ {
		if err := e.Handle(ctx, started(id)); err != nil {
			t.Fatal(err)
		}
	}
	if b, _, _ := fk.Counts(id); b != 1 {
		t.Fatalf("begin count = %d, want 1", b)
	}
	if obs.begun != 1 {
		t.Fatalf("observer begun = %d, want 1", obs.begun)
	}
}

func TestCommitOnHealthy(t *testing.T) {
	e, fk, obs, _ := newTestEngine(t)
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"

	must(t, e.Handle(ctx, started(id)))
	must(t, e.Handle(ctx, Signal{Kind: SignalSucceeded, ID: id}))

	if st, _ := fk.State(id); st != txn.StateCommitted {
		t.Fatalf("state = %s, want committed", st)
	}
	if obs.committed != 1 {
		t.Fatalf("committed = %d, want 1", obs.committed)
	}
	// A second commit is a no-op.
	must(t, e.Handle(ctx, Signal{Kind: SignalSucceeded, ID: id}))
	if _, c, _ := fk.Counts(id); c != 1 {
		t.Fatalf("commit count = %d, want 1", c)
	}
}

func TestRollbackOnFailed(t *testing.T) {
	e, fk, obs, _ := newTestEngine(t)
	ctx := context.Background()
	id := "flux/apps/podinfo/r1"

	must(t, e.Handle(ctx, started(id)))
	must(t, e.Handle(ctx, Signal{Kind: SignalFailed, ID: id, Reason: "ImagePullBackOff"}))

	if st, _ := fk.State(id); st != txn.StateRolledBack {
		t.Fatalf("state = %s, want rolledBack", st)
	}
	if obs.rolledBack != 1 {
		t.Fatalf("rolledBack = %d, want 1", obs.rolledBack)
	}
}

func TestRollbackOnDeadline(t *testing.T) {
	e, fk, obs, timer := newTestEngine(t)
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"

	must(t, e.Handle(ctx, started(id)))
	if *timer == nil {
		t.Fatal("deadline timer was not armed on begin")
	}
	// Fire the health deadline.
	(*timer).fire()

	if st, _ := fk.State(id); st != txn.StateRolledBack {
		t.Fatalf("state = %s, want rolledBack after deadline", st)
	}
	if obs.rolledBack != 1 {
		t.Fatalf("rolledBack = %d, want 1", obs.rolledBack)
	}
}

func TestDeadlineCancelledAfterCommit(t *testing.T) {
	e, fk, _, timer := newTestEngine(t)
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"

	must(t, e.Handle(ctx, started(id)))
	must(t, e.Handle(ctx, Signal{Kind: SignalSucceeded, ID: id}))

	if !(*timer).stopped {
		t.Fatal("deadline timer should be stopped after commit")
	}
	// Firing a stale timer must not roll back a committed transaction.
	(*timer).fire()
	if st, _ := fk.State(id); st != txn.StateCommitted {
		t.Fatalf("state = %s, want committed (stale deadline must not roll back)", st)
	}
}

func TestIdempotentRestartAdoptsOpenTxn(t *testing.T) {
	e, fk, _, _ := newTestEngine(t)
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"

	// A previous controller instance already opened this transaction.
	fk.Seed(id, txn.StateOpen)

	// The restarted controller sees the same rollout: it must NOT begin again.
	must(t, e.Handle(ctx, started(id)))
	if b, _, _ := fk.Counts(id); b != 0 {
		t.Fatalf("begin count = %d, want 0 (adopted existing open txn)", b)
	}
	// It should still drive the adopted transaction to completion.
	must(t, e.Handle(ctx, Signal{Kind: SignalSucceeded, ID: id}))
	if st, _ := fk.State(id); st != txn.StateCommitted {
		t.Fatalf("state = %s, want committed", st)
	}
}

func TestNeverBeginFinishedTxn(t *testing.T) {
	e, fk, _, _ := newTestEngine(t)
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"
	fk.Seed(id, txn.StateCommitted)

	must(t, e.Handle(ctx, started(id)))
	if b, _, _ := fk.Counts(id); b != 0 {
		t.Fatalf("begin count = %d, want 0 (already committed)", b)
	}
}

func TestNeverRollbackCommitted(t *testing.T) {
	e, fk, obs, _ := newTestEngine(t)
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"
	fk.Seed(id, txn.StateCommitted)

	must(t, e.Handle(ctx, Signal{Kind: SignalFailed, ID: id, Reason: "late failure"}))
	if _, _, rb := fk.Counts(id); rb != 0 {
		t.Fatalf("rollback count = %d, want 0 (must never roll back a committed txn)", rb)
	}
	if st, _ := fk.State(id); st != txn.StateCommitted {
		t.Fatalf("state = %s, want committed", st)
	}
	if obs.rolledBack != 0 {
		t.Fatalf("rolledBack = %d, want 0", obs.rolledBack)
	}
}

func TestDryRunSendsNothing(t *testing.T) {
	fk := txntest.New("")
	srv := fk.Server()
	t.Cleanup(srv.Close)
	client, _ := txn.New(srv.URL)
	e := NewEngine(Config{Client: client, DryRun: true, HealthDeadline: time.Hour})
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"

	must(t, e.Handle(ctx, started(id)))
	must(t, e.Handle(ctx, Signal{Kind: SignalSucceeded, ID: id}))
	if b, c, _ := fk.Counts(id); b != 0 || c != 0 {
		t.Fatalf("dry-run sent calls: begin=%d commit=%d", b, c)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// recordingAnnotator captures outcome write-backs.
type recordingAnnotator struct {
	mu      sync.Mutex
	records []string // "<id>=<outcome>"
}

func (a *recordingAnnotator) Record(_ context.Context, ref ObjRef, id string, outcome Outcome) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, ref.Name+":"+id+"="+string(outcome))
	return nil
}

func TestInProgressAdoptsOpenTxnAndArmsDeadline(t *testing.T) {
	e, fk, obs, timer := newTestEngine(t)
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"
	fk.Seed(id, txn.StateOpen)

	// After a restart the controller only ever sees Succeeded+Progressing.
	must(t, e.Handle(ctx, Signal{Kind: SignalInProgress, ID: id, Ref: ObjRef{Namespace: "apps", Name: "podinfo"}}))
	if len(e.Active()) != 1 || *timer == nil {
		t.Fatalf("open txn not adopted: active=%v timer=%v", e.Active(), *timer)
	}
	if b, _, _ := fk.Counts(id); b != 0 {
		t.Fatalf("begin count = %d, want 0", b)
	}
	// A second in-progress event is a no-op.
	must(t, e.Handle(ctx, Signal{Kind: SignalInProgress, ID: id}))
	// The re-armed deadline rolls back.
	(*timer).fire()
	if st, _ := fk.State(id); st != txn.StateRolledBack {
		t.Fatalf("state after deadline = %s, want rolledBack", st)
	}
	if obs.rolledBack != 1 {
		t.Fatalf("rolledBack = %d, want 1", obs.rolledBack)
	}
}

func TestFinishedTxnOutcomeIsRecordedOnceAfterRestart(t *testing.T) {
	e, fk, _, _ := newTestEngine(t)
	ann := &recordingAnnotator{}
	e.cfg.Annotator = ann
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"
	fk.Seed(id, txn.StateCommitted)
	ref := ObjRef{Namespace: "apps", Name: "podinfo"}

	// Succeeded+Healthy for a transaction a previous instance committed: the
	// outcome is written back (with the signal's ref), exactly once.
	must(t, e.Handle(ctx, Signal{Kind: SignalSucceeded, ID: id, Ref: ref}))
	must(t, e.Handle(ctx, Signal{Kind: SignalSucceeded, ID: id, Ref: ref}))
	must(t, e.Handle(ctx, Signal{Kind: SignalInProgress, ID: id, Ref: ref}))
	if len(ann.records) != 1 || ann.records[0] != "podinfo:"+id+"=committed" {
		t.Fatalf("records = %v, want one committed write-back", ann.records)
	}
	if _, c, _ := fk.Counts(id); c != 0 {
		t.Fatalf("commit count = %d, want 0 (ledger already committed)", c)
	}
}

// flakyClient fails the first n calls of every method with a transport-style
// error, then delegates.
type flakyClient struct {
	TxnClient
	mu    sync.Mutex
	fails int
}

func (f *flakyClient) Get(ctx context.Context, id string) (*txn.Transaction, error) {
	f.mu.Lock()
	if f.fails > 0 {
		f.fails--
		f.mu.Unlock()
		return nil, context.DeadlineExceeded
	}
	f.mu.Unlock()
	return f.TxnClient.Get(ctx, id)
}

func TestDeadlineRollbackRetriesUntilKineAnswers(t *testing.T) {
	e, fk, obs, timer := newTestEngine(t)
	e.cfg.retryBackoff = time.Millisecond
	e.cfg.deadlineRetries = 10
	ctx := context.Background()
	id := "argo/apps/podinfo/r1"
	must(t, e.Handle(ctx, started(id)))
	// kine becomes unreachable after begin: the first three rollback attempts fail.
	e.cfg.Client = &flakyClient{TxnClient: e.cfg.Client, fails: 3}
	(*timer).fire()
	if st, _ := fk.State(id); st != txn.StateRolledBack {
		t.Fatalf("state after deadline = %s, want rolledBack (retries exhausted the flaky client)", st)
	}
	if obs.rolledBack != 1 {
		t.Fatalf("rolledBack = %d, want 1", obs.rolledBack)
	}
}
