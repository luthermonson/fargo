package argo_test

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/luthermonson/fargo/pkg/modes"
	"github.com/luthermonson/fargo/pkg/modes/argo"
	"github.com/luthermonson/fargo/pkg/reconcile"
)

// fakeResolver maps kinds to resources without a cluster.
type fakeResolver map[string]string

func (f fakeResolver) ResourceFor(_, kind string) (string, error) {
	if r, ok := f[kind]; ok {
		return r, nil
	}
	return "unknowns", nil
}

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

const runningApp = `{
  "apiVersion": "argoproj.io/v1alpha1",
  "kind": "Application",
  "metadata": {"name": "podinfo", "namespace": "argocd"},
  "spec": {"destination": {"namespace": "demo"}},
  "status": {
    "operationState": {
      "phase": "Running",
      "operation": {"sync": {"revision": "abc123def"}}
    },
    "resources": [
      {"group": "apps", "kind": "Deployment", "namespace": "demo", "name": "podinfo"},
      {"group": "", "kind": "Service", "namespace": "demo", "name": "podinfo"},
      {"group": "", "kind": "ConfigMap", "namespace": "demo", "name": "podinfo-cfg"}
    ]
  }
}`

func TestArgoStartedScope(t *testing.T) {
	m := argo.New(fakeResolver{"Deployment": "deployments", "Service": "services", "ConfigMap": "configmaps"}, modes.NamespaceScopeAuto)
	sig, err := m.Classify(fromJSON(t, runningApp))
	if err != nil {
		t.Fatal(err)
	}
	if sig.Kind != reconcile.SignalStarted {
		t.Fatalf("kind = %v, want Started", sig.Kind)
	}
	if sig.ID != "argo/argocd/podinfo/abc123def" {
		t.Fatalf("id = %q", sig.ID)
	}
	wantKeys := map[string]bool{
		"apps/deployments/demo/podinfo": true,
		"services/demo/podinfo":         true,
		"configmaps/demo/podinfo-cfg":   true,
	}
	for _, k := range sig.Scope.Keys {
		if !wantKeys[k] {
			t.Errorf("unexpected scope key %q", k)
		}
		delete(wantKeys, k)
	}
	if len(wantKeys) != 0 {
		t.Errorf("missing scope keys: %v", wantKeys)
	}
	if len(sig.Scope.Namespaces) != 1 || sig.Scope.Namespaces[0] != "demo" {
		t.Errorf("namespaces = %v, want [demo]", sig.Scope.Namespaces)
	}
	if !sig.Scope.ExpandOwners {
		t.Error("expandOwners should be true")
	}
}

func TestArgoNamespaceScopeNone(t *testing.T) {
	m := argo.New(fakeResolver{"Deployment": "deployments", "Service": "services", "ConfigMap": "configmaps"}, modes.NamespaceScopeNone)
	sig, _ := m.Classify(fromJSON(t, runningApp))
	if len(sig.Scope.Namespaces) != 0 {
		t.Errorf("namespaces = %v, want none", sig.Scope.Namespaces)
	}
}

func TestArgoSucceededHealthy(t *testing.T) {
	m := argo.New(fakeResolver{}, modes.NamespaceScopeAuto)
	app := fromJSON(t, `{
      "metadata": {"name": "podinfo", "namespace": "argocd"},
      "status": {
        "operationState": {"phase": "Succeeded", "operation": {"sync": {"revision": "r9"}}},
        "health": {"status": "Healthy"}
      }}`)
	sig, _ := m.Classify(app)
	if sig.Kind != reconcile.SignalSucceeded || sig.ID != "argo/argocd/podinfo/r9" {
		t.Fatalf("got %+v", sig)
	}
}

func TestArgoSucceededNotYetHealthy(t *testing.T) {
	m := argo.New(fakeResolver{}, modes.NamespaceScopeAuto)
	app := fromJSON(t, `{
      "metadata": {"name": "podinfo", "namespace": "argocd"},
      "status": {"operationState": {"phase": "Succeeded"}, "health": {"status": "Progressing"}}}`)
	sig, _ := m.Classify(app)
	if sig.Kind != reconcile.SignalInProgress {
		t.Fatalf("synced-but-not-healthy should be in progress (no action, adoptable), got %v", sig.Kind)
	}
	if sig.ID == "" || sig.Ref.Name != "podinfo" {
		t.Fatalf("in-progress signal must carry the id and ref: %+v", sig)
	}
}

func TestArgoFailed(t *testing.T) {
	m := argo.New(fakeResolver{}, modes.NamespaceScopeAuto)
	for _, phase := range []string{"Failed", "Error"} {
		app := fromJSON(t, `{
          "metadata": {"name": "podinfo", "namespace": "argocd"},
          "status": {"operationState": {"phase": "`+phase+`", "operation": {"sync": {"revision": "r1"}}}}}`)
		sig, _ := m.Classify(app)
		if sig.Kind != reconcile.SignalFailed {
			t.Errorf("phase %s: kind = %v, want Failed", phase, sig.Kind)
		}
	}
}

func TestArgoDegradedAfterSync(t *testing.T) {
	m := argo.New(fakeResolver{}, modes.NamespaceScopeAuto)
	app := fromJSON(t, `{
      "metadata": {"name": "podinfo", "namespace": "argocd"},
      "status": {"operationState": {"phase": "Succeeded", "operation": {"sync": {"revision": "r1"}}}, "health": {"status": "Degraded"}}}`)
	sig, _ := m.Classify(app)
	if sig.Kind != reconcile.SignalFailed {
		t.Fatalf("degraded should fail, got %v", sig.Kind)
	}
}

func TestArgoRunningWithoutSyncIsNone(t *testing.T) {
	m := argo.New(fakeResolver{}, modes.NamespaceScopeAuto)
	app := fromJSON(t, `{"metadata":{"name":"x","namespace":"argocd"},"status":{"operationState":{"phase":"Running"}}}`)
	sig, _ := m.Classify(app)
	if sig.Kind != reconcile.SignalNone {
		t.Fatalf("Running without a sync operation should be None, got %v", sig.Kind)
	}
}

func TestArgoStaleHealthAfterSyncIsInProgress(t *testing.T) {
	m := argo.New(fakeResolver{}, modes.NamespaceScopeAuto)
	// The operation finished at :39 but the last reconcile ran at :10, so the
	// Healthy status is the previous rollout's. Must not commit.
	stale := fromJSON(t, `{
      "metadata": {"name": "podinfo", "namespace": "argocd"},
      "status": {
        "operationState": {"phase": "Succeeded", "finishedAt": "2026-11-20T02:53:39Z", "operation": {"sync": {"revision": "r2"}}},
        "reconciledAt": "2026-11-20T02:53:10Z",
        "sync": {"status": "Synced", "revision": "r1"},
        "health": {"status": "Healthy"}}}`)
	sig, _ := m.Classify(stale)
	if sig.Kind != reconcile.SignalInProgress || sig.ID != "argo/argocd/podinfo/r2" {
		t.Fatalf("stale Healthy after sync should be in progress, got %v %q", sig.Kind, sig.ID)
	}
	// Reconciled after the operation, for the operation's revision: trusted.
	fresh := fromJSON(t, `{
      "metadata": {"name": "podinfo", "namespace": "argocd"},
      "status": {
        "operationState": {"phase": "Succeeded", "finishedAt": "2026-11-20T02:53:39Z", "operation": {"sync": {"revision": "r2"}}},
        "reconciledAt": "2026-11-20T02:54:19Z",
        "sync": {"status": "Synced", "revision": "r2"},
        "health": {"status": "Healthy"}}}`)
	sig, _ = m.Classify(fresh)
	if sig.Kind != reconcile.SignalSucceeded {
		t.Fatalf("fresh Healthy after sync should commit, got %v", sig.Kind)
	}
	// Reconciled after the operation but describing another revision: stale.
	other := fromJSON(t, `{
      "metadata": {"name": "podinfo", "namespace": "argocd"},
      "status": {
        "operationState": {"phase": "Succeeded", "finishedAt": "2026-11-20T02:53:39Z", "operation": {"sync": {"revision": "r2"}}},
        "reconciledAt": "2026-11-20T02:54:19Z",
        "sync": {"status": "Synced", "revision": "r1"},
        "health": {"status": "Degraded"}}}`)
	sig, _ = m.Classify(other)
	if sig.Kind != reconcile.SignalInProgress {
		t.Fatalf("Degraded for a different revision must not roll back, got %v", sig.Kind)
	}
}
