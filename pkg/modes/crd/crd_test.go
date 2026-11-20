package crd_test

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/luthermonson/fargo/pkg/modes/crd"
	"github.com/luthermonson/fargo/pkg/reconcile"
)

func fromJSON(t *testing.T, s string) *unstructured.Unstructured {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad fixture json: %v", err)
	}
	// JSON numbers decode to float64; coerce metadata.generation to the int64
	// the unstructured accessors (and the controller) expect.
	if md, ok := m["metadata"].(map[string]any); ok {
		if g, ok := md["generation"].(float64); ok {
			md["generation"] = int64(g)
		}
	}
	return &unstructured.Unstructured{Object: m}
}

const pendingTxn = `{
  "apiVersion": "fargo.gascity.dev/v1alpha1",
  "kind": "Transaction",
  "metadata": {"name": "deploy-podinfo", "namespace": "ci", "generation": 1},
  "spec": {
    "scope": {
      "keys": ["apps/deployments/demo/podinfo"],
      "prefixes": ["/registry/services/demo/"],
      "namespaces": ["demo"],
      "expandOwners": true
    },
    "outcome": ""
  }
}`

func TestCRDCreateBegins(t *testing.T) {
	m := crd.New()
	sig, err := m.Classify(fromJSON(t, pendingTxn))
	if err != nil {
		t.Fatal(err)
	}
	if sig.Kind != reconcile.SignalStarted {
		t.Fatalf("kind = %v, want Started", sig.Kind)
	}
	if sig.ID != "crd/ci/deploy-podinfo/1" {
		t.Fatalf("id = %q", sig.ID)
	}
	if len(sig.Scope.Keys) != 1 || sig.Scope.Keys[0] != "apps/deployments/demo/podinfo" {
		t.Errorf("keys = %v", sig.Scope.Keys)
	}
	if len(sig.Scope.Prefixes) != 1 || len(sig.Scope.Namespaces) != 1 || !sig.Scope.ExpandOwners {
		t.Errorf("scope = %+v", sig.Scope)
	}
	if sig.Ref.GVRResource != "transactions" {
		t.Errorf("GVR resource = %q", sig.Ref.GVRResource)
	}
}

func TestCRDOutcomeCommit(t *testing.T) {
	m := crd.New()
	obj := fromJSON(t, `{"metadata":{"name":"t","namespace":"ci","generation":2},"spec":{"outcome":"commit"},"status":{"phase":"Open"}}`)
	sig, _ := m.Classify(obj)
	if sig.Kind != reconcile.SignalSucceeded || sig.ID != "crd/ci/t/2" {
		t.Fatalf("got %+v", sig)
	}
}

func TestCRDOutcomeRollback(t *testing.T) {
	m := crd.New()
	obj := fromJSON(t, `{"metadata":{"name":"t","namespace":"ci","generation":3},"spec":{"outcome":"rollback"},"status":{"phase":"Open"}}`)
	sig, _ := m.Classify(obj)
	if sig.Kind != reconcile.SignalFailed {
		t.Fatalf("kind = %v, want Failed", sig.Kind)
	}
}

func TestCRDFinishedIsNone(t *testing.T) {
	m := crd.New()
	for _, phase := range []string{"Committed", "RolledBack", "Failed"} {
		obj := fromJSON(t, `{"metadata":{"name":"t","namespace":"ci","generation":1},"spec":{"outcome":""},"status":{"phase":"`+phase+`"}}`)
		if sig, _ := m.Classify(obj); sig.Kind != reconcile.SignalNone {
			t.Errorf("phase %s: want None, got %v", phase, sig.Kind)
		}
	}
}

func TestCRDDeleteWhileOpenRollsBack(t *testing.T) {
	m := crd.New()
	obj := fromJSON(t, `{"metadata":{"name":"t","namespace":"ci","generation":1},"status":{"phase":"Open"}}`)
	sig, actionable := m.OnDelete(obj)
	if !actionable || sig.Kind != reconcile.SignalFailed {
		t.Fatalf("delete while open: actionable=%v sig=%+v", actionable, sig)
	}
}

func TestCRDDeleteWhileFinishedIsNoop(t *testing.T) {
	m := crd.New()
	obj := fromJSON(t, `{"metadata":{"name":"t","namespace":"ci","generation":1},"status":{"phase":"Committed"}}`)
	if _, actionable := m.OnDelete(obj); actionable {
		t.Fatal("delete of a committed transaction must be a no-op")
	}
}
