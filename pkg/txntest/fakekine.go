// Package txntest provides a fake, in-memory implementation of the kine
// control API v0, served over httptest, so the client and the reconcile engine
// can be tested without a real kine. It lives in its own package (not a _test
// file) so multiple test packages can share it.
package txntest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/luthermonson/rewind/pkg/txn"
)

// FakeKine is an in-memory kine control API.
type FakeKine struct {
	mu      sync.Mutex
	ledger  map[string]*txn.Transaction
	nextRev int64
	token   string

	beginCount    map[string]int
	commitCount   map[string]int
	rollbackCount map[string]int
}

// New builds a FakeKine. token, if non-empty, is required as a bearer.
func New(token string) *FakeKine {
	return &FakeKine{
		ledger:        map[string]*txn.Transaction{},
		nextRev:       100,
		token:         token,
		beginCount:    map[string]int{},
		commitCount:   map[string]int{},
		rollbackCount: map[string]int{},
	}
}

// Server starts an httptest.Server for the fake. Caller closes it.
func (f *FakeKine) Server() *httptest.Server { return httptest.NewServer(f) }

func (f *FakeKine) writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ServeHTTP implements the subset of the API the controller uses.
func (f *FakeKine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		f.writeErr(w, http.StatusUnauthorized, "bad token")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Transaction ids contain slashes (<mode>/<ns>/<name>/<rev>), so routing is
	// by prefix/suffix, not path-segment count, exactly as real kine must do.
	path := strings.TrimPrefix(r.URL.Path, "/")

	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && path == "txn":
		out := make([]txn.Transaction, 0, len(f.ledger))
		for _, t := range f.ledger {
			out = append(out, *t)
		}
		_ = json.NewEncoder(w).Encode(out)

	case r.Method == http.MethodPost && path == "txn/begin":
		var req txn.BeginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.writeErr(w, http.StatusBadRequest, "bad body")
			return
		}
		if existing, ok := f.ledger[req.ID]; ok && existing.State == txn.StateOpen {
			f.writeErr(w, http.StatusConflict, "transaction already open")
			return
		}
		f.nextRev++
		sc := req.Scope
		t := &txn.Transaction{ID: req.ID, State: txn.StateOpen, BaseRev: f.nextRev, BaseCommit: "commit-abc", StartedAt: "now", Scope: &sc}
		f.ledger[req.ID] = t
		f.beginCount[req.ID]++
		_ = json.NewEncoder(w).Encode(t)

	case r.Method == http.MethodPost && strings.HasPrefix(path, "txn/") && strings.HasSuffix(path, "/commit"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "txn/"), "/commit")
		t, ok := f.ledger[id]
		if !ok {
			f.writeErr(w, http.StatusNotFound, "no such transaction")
			return
		}
		t.State = txn.StateCommitted
		f.commitCount[id]++
		_ = json.NewEncoder(w).Encode(t)

	case r.Method == http.MethodPost && strings.HasPrefix(path, "txn/") && strings.HasSuffix(path, "/rollback"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "txn/"), "/rollback")
		t, ok := f.ledger[id]
		if !ok {
			f.writeErr(w, http.StatusNotFound, "no such transaction")
			return
		}
		t.State = txn.StateRolledBack
		f.rollbackCount[id]++
		_ = json.NewEncoder(w).Encode(txn.RollbackResult{ID: t.ID, State: txn.StateRolledBack, Events: 3, FreezeMs: 12, Reverted: []string{"a", "b", "c"}})

	case r.Method == http.MethodGet && strings.HasPrefix(path, "txn/"):
		id := strings.TrimPrefix(path, "txn/")
		t, ok := f.ledger[id]
		if !ok {
			f.writeErr(w, http.StatusNotFound, "no such transaction")
			return
		}
		_ = json.NewEncoder(w).Encode(t)

	default:
		f.writeErr(w, http.StatusNotFound, "unknown route "+r.Method+" "+path)
	}
}

// Seed inserts a transaction directly into the ledger, simulating a prior
// controller instance (for restart/idempotency tests).
func (f *FakeKine) Seed(id string, state txn.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRev++
	f.ledger[id] = &txn.Transaction{ID: id, State: state, BaseRev: f.nextRev}
}

// State returns the current ledger state for an id, and whether it exists.
func (f *FakeKine) State(id string) (txn.State, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.ledger[id]
	if !ok {
		return "", false
	}
	return t.State, true
}

// Counts returns call counts for an id: begins, commits, rollbacks.
func (f *FakeKine) Counts(id string) (begin, commit, rollback int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.beginCount[id], f.commitCount[id], f.rollbackCount[id]
}
