package flux_test

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/luthermonson/fargo/pkg/modes"
	"github.com/luthermonson/fargo/pkg/modes/flux"
	"github.com/luthermonson/fargo/pkg/reconcile"
)

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

const reconcilingKustomization = `{
  "apiVersion": "kustomize.toolkit.fluxcd.io/v1",
  "kind": "Kustomization",
  "metadata": {"name": "podinfo", "namespace": "flux-system"},
  "spec": {"targetNamespace": "demo"},
  "status": {
    "lastAttemptedRevision": "main@sha1:abcdef",
    "lastAppliedRevision": "main@sha1:000000",
    "conditions": [
      {"type": "Ready", "status": "Unknown", "reason": "Progressing"},
      {"type": "Reconciling", "status": "True", "reason": "Progressing"}
    ],
    "inventory": {"entries": [
      {"id": "demo_podinfo_apps_Deployment", "v": "v1"},
      {"id": "demo_podinfo__Service", "v": "v1"},
      {"id": "_podinfo-cr_rbac.authorization.k8s.io_ClusterRole", "v": "v1"}
    ]}
  }
}`

func TestFluxStarted(t *testing.T) {
	m := flux.New(fakeResolver{"Deployment": "deployments", "Service": "services", "ClusterRole": "clusterroles"}, modes.NamespaceScopeAuto)
	sig, err := m.Classify(fromJSON(t, reconcilingKustomization))
	if err != nil {
		t.Fatal(err)
	}
	if sig.Kind != reconcile.SignalStarted {
		t.Fatalf("kind = %v, want Started", sig.Kind)
	}
	if sig.ID != "flux/flux-system/podinfo/main-sha1-abcdef" {
		t.Fatalf("id = %q", sig.ID)
	}
	want := map[string]bool{
		"apps/deployments/demo/podinfo":                     true,
		"services/demo/podinfo":                             true,
		"rbac.authorization.k8s.io/clusterroles/podinfo-cr": true,
	}
	for _, k := range sig.Scope.Keys {
		if !want[k] {
			t.Errorf("unexpected key %q", k)
		}
		delete(want, k)
	}
	if len(want) != 0 {
		t.Errorf("missing keys: %v", want)
	}
	if len(sig.Scope.Namespaces) != 1 || sig.Scope.Namespaces[0] != "demo" {
		t.Errorf("namespaces = %v, want [demo]", sig.Scope.Namespaces)
	}
}

func TestFluxSucceeded(t *testing.T) {
	m := flux.New(fakeResolver{}, modes.NamespaceScopeAuto)
	k := fromJSON(t, `{
      "kind": "Kustomization",
      "metadata": {"name": "podinfo", "namespace": "flux-system"},
      "status": {
        "lastAttemptedRevision": "r5",
        "lastAppliedRevision": "r5",
        "conditions": [{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded"}]
      }}`)
	sig, _ := m.Classify(k)
	if sig.Kind != reconcile.SignalSucceeded || sig.ID != "flux/flux-system/podinfo/r5" {
		t.Fatalf("got %+v", sig)
	}
}

func TestFluxFailed(t *testing.T) {
	m := flux.New(fakeResolver{}, modes.NamespaceScopeAuto)
	for _, reason := range []string{"BuildFailed", "HealthCheckFailed", "InstallFailed", "UpgradeFailed", "ReconciliationFailed"} {
		k := fromJSON(t, `{
          "kind": "Kustomization",
          "metadata": {"name": "podinfo", "namespace": "flux-system"},
          "status": {"lastAttemptedRevision": "r5",
            "conditions": [{"type": "Ready", "status": "False", "reason": "`+reason+`"}]}}`)
		sig, _ := m.Classify(k)
		if sig.Kind != reconcile.SignalFailed {
			t.Errorf("reason %s: kind = %v, want Failed", reason, sig.Kind)
		}
	}
}

func TestFluxHelmReleaseGVR(t *testing.T) {
	m := flux.New(fakeResolver{}, modes.NamespaceScopeAuto)
	hr := fromJSON(t, `{
      "apiVersion": "helm.toolkit.fluxcd.io/v2",
      "kind": "HelmRelease",
      "metadata": {"name": "podinfo", "namespace": "flux-system"},
      "status": {"lastAttemptedRevision": "6.17.0",
        "conditions": [{"type": "Ready", "status": "Unknown", "reason": "Progressing"},
                       {"type": "Reconciling", "status": "True"}]}}`)
	sig, _ := m.Classify(hr)
	if sig.Kind != reconcile.SignalStarted {
		t.Fatalf("kind = %v, want Started", sig.Kind)
	}
	if sig.Ref.GVRResource != "helmreleases" || sig.Ref.GVRGroup != "helm.toolkit.fluxcd.io" {
		t.Fatalf("GVR = %s/%s/%s", sig.Ref.GVRGroup, sig.Ref.GVRVersion, sig.Ref.GVRResource)
	}
}

func TestFluxIdleIsNone(t *testing.T) {
	m := flux.New(fakeResolver{}, modes.NamespaceScopeAuto)
	k := fromJSON(t, `{
      "kind": "Kustomization",
      "metadata": {"name": "podinfo", "namespace": "flux-system"},
      "status": {"lastAttemptedRevision": "r5", "lastAppliedRevision": "r5",
        "conditions": [{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded"},
                       {"type": "Reconciling", "status": "False"}]}}`)
	// applied==attempted and Ready=True -> Succeeded, not None; verify a truly
	// idle object (no attempt) is None.
	idle := fromJSON(t, `{"kind":"Kustomization","metadata":{"name":"x","namespace":"flux-system"},"status":{}}`)
	if sig, _ := m.Classify(idle); sig.Kind != reconcile.SignalNone {
		t.Fatalf("idle object should be None, got %v", sig.Kind)
	}
	if sig, _ := m.Classify(k); sig.Kind != reconcile.SignalSucceeded {
		t.Fatalf("settled object should be Succeeded, got %v", sig.Kind)
	}
}
