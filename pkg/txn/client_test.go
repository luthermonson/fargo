package txn_test

import (
	"context"
	"testing"

	"github.com/luthermonson/rewind/pkg/txn"
	"github.com/luthermonson/rewind/pkg/txntest"
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
