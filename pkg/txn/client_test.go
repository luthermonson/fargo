package txn_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luthermonson/fargo/pkg/txn"
	"github.com/luthermonson/fargo/pkg/txntest"
)

func TestClientLifecycleOverHTTP(t *testing.T) {
	fk := txntest.New("sekret")
	srv := fk.Server()
	defer srv.Close()

	c, err := txn.New(srv.URL, txn.WithToken("sekret"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Not found before begin.
	if _, err := c.Get(ctx, "argo/apps/podinfo/abc"); !txn.IsNotFound(err) {
		t.Fatalf("want NotFound, got %v", err)
	}

	scope := txn.Scope{Keys: []string{"apps/deployments/apps/podinfo"}, Namespaces: []string{"apps"}, ExpandOwners: true}
	tr, err := c.Begin(ctx, "argo/apps/podinfo/abc", scope)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if tr.State != txn.StateOpen || tr.BaseRev == 0 {
		t.Fatalf("unexpected begin result: %+v", tr)
	}

	// Begin again for the same open id is a conflict.
	if _, err := c.Begin(ctx, "argo/apps/podinfo/abc", scope); !txn.IsConflict(err) {
		t.Fatalf("want Conflict on double begin, got %v", err)
	}

	got, err := c.Get(ctx, "argo/apps/podinfo/abc")
	if err != nil || got.State != txn.StateOpen {
		t.Fatalf("get after begin: %+v %v", got, err)
	}

	list, err := c.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v (%d)", err, len(list))
	}

	done, err := c.Commit(ctx, "argo/apps/podinfo/abc")
	if err != nil || done.State != txn.StateCommitted {
		t.Fatalf("commit: %+v %v", done, err)
	}
}

func TestClientRollbackResult(t *testing.T) {
	fk := txntest.New("")
	srv := fk.Server()
	defer srv.Close()

	c, _ := txn.New(srv.URL)
	ctx := context.Background()
	if _, err := c.Begin(ctx, "crd/ns/t1/1", txn.Scope{Prefixes: []string{"/registry/deployments/ns/"}}); err != nil {
		t.Fatal(err)
	}
	res, err := c.Rollback(ctx, "crd/ns/t1/1")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if res.State != txn.StateRolledBack || res.Events != 3 || len(res.Reverted) != 3 {
		t.Fatalf("unexpected rollback result: %+v", res)
	}
}

func TestClientTokenRequired(t *testing.T) {
	fk := txntest.New("needed")
	srv := fk.Server()
	defer srv.Close()

	c, _ := txn.New(srv.URL) // no token
	if _, err := c.List(context.Background()); err == nil {
		t.Fatal("expected auth failure without token")
	}
}

func TestNewRejectsBadEndpoints(t *testing.T) {
	for _, ep := range []string{"", "ftp://x", "host:1234"} {
		if _, err := txn.New(ep); err == nil {
			t.Errorf("expected error for endpoint %q", ep)
		}
	}
	if _, err := txn.New("unix:///tmp/kine.sock"); err != nil {
		t.Errorf("unix endpoint should be accepted: %v", err)
	}
}

func TestClientRetriesTransportErrors(t *testing.T) {
	// The first two requests hang past the client's timeout; the third answers.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			time.Sleep(300 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c, err := txn.New(srv.URL, txn.WithHTTPClient(&http.Client{Timeout: 100 * time.Millisecond}), txn.WithRetries(3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List should succeed after retries: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("server saw %d calls, want 3", got)
	}
	// With no retries the same client fails on the first hang.
	atomic.StoreInt32(&calls, 0)
	c2, _ := txn.New(srv.URL, txn.WithHTTPClient(&http.Client{Timeout: 100 * time.Millisecond}), txn.WithRetries(0))
	if _, err := c2.List(context.Background()); err == nil {
		t.Fatal("List without retries should fail on a hung request")
	}
	// An HTTP error is never retried.
	var httpErrCalls int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&httpErrCalls, 1)
		http.Error(w, `{"error":"nope"}`, http.StatusConflict)
	}))
	defer bad.Close()
	c3, _ := txn.New(bad.URL, txn.WithRetries(3))
	if _, err := c3.Begin(context.Background(), "x", txn.Scope{Namespaces: []string{"a"}}); !txn.IsConflict(err) {
		t.Fatalf("want 409 conflict, got %v", err)
	}
	if atomic.LoadInt32(&httpErrCalls) != 1 {
		t.Fatalf("HTTP errors must not be retried; saw %d calls", httpErrCalls)
	}
}
