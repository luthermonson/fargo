// Package flux maps Flux Kustomizations and HelmReleases to rollout Signals.
//
// Begin: status.lastAttemptedRevision is set and the object is reconciling
// (Reconciling=True, or Ready=Unknown with reason Progressing). Because the
// transaction id embeds the attempted revision, a new revision is a new id and
// begin is idempotent, so "the attempted revision changed" needs no prior
// state. Scope: status.inventory.entries[].id (<namespace>_<name>_<group>_<kind>)
// plus spec.targetNamespace / the release namespace. Commit: Ready=True with
// lastAppliedRevision == lastAttemptedRevision. Rollback: Ready=False
// (BuildFailed, HealthCheckFailed, InstallFailed, UpgradeFailed,
// ReconciliationFailed, ...); the deadline is the engine's.
package flux

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/luthermonson/fargo/pkg/modes"
	"github.com/luthermonson/fargo/pkg/reconcile"
	"github.com/luthermonson/fargo/pkg/txn"
)

// GVRs are the Flux resources fargo watches.
var (
	KustomizationGVR = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	HelmReleaseGVR   = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
)

// Mapper implements modes.Mapper for Flux.
type Mapper struct {
	modes.NoDelete
	resolver modes.ResourceResolver
	nsScope  modes.NamespaceScope
}

// New builds a Flux mapper.
func New(resolver modes.ResourceResolver, nsScope modes.NamespaceScope) *Mapper {
	return &Mapper{resolver: resolver, nsScope: nsScope}
}

// Resources returns the Kustomization and HelmRelease GVRs.
func (m *Mapper) Resources() []schema.GroupVersionResource {
	return []schema.GroupVersionResource{KustomizationGVR, HelmReleaseGVR}
}

// Classify maps a Kustomization or HelmRelease to a rollout Signal.
func (m *Mapper) Classify(obj *unstructured.Unstructured) (reconcile.Signal, error) {
	name := obj.GetName()
	ns := obj.GetNamespace()
	gvr := gvrFor(obj)
	ref := reconcile.ObjRef{
		Namespace:   ns,
		Name:        name,
		GVRGroup:    gvr.Group,
		GVRVersion:  gvr.Version,
		GVRResource: gvr.Resource,
	}

	attempted, _, _ := unstructured.NestedString(obj.Object, "status", "lastAttemptedRevision")
	applied, _, _ := unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")
	id := reconcile.SanitizeID("flux", ns, name, attempted)

	ready, readyReason := condition(obj, "Ready")
	reconciling, _ := condition(obj, "Reconciling")

	// Failure takes precedence: Ready=False with a terminal reason.
	if ready == "False" {
		return reconcile.Signal{Kind: reconcile.SignalFailed, ID: id, Ref: ref, Reason: "Ready=False: " + readyReason}, nil
	}

	// Success: Ready=True and the applied revision caught up to the attempt.
	if ready == "True" && attempted != "" && applied == attempted {
		return reconcile.Signal{Kind: reconcile.SignalSucceeded, ID: id, Ref: ref}, nil
	}

	// Started: an attempt is in flight. React on the reconciling condition
	// immediately to narrow the window before apply (Flux has no pre-apply
	// hook).
	if attempted != "" && (reconciling == "True" || (ready == "Unknown" && isProgressing(readyReason))) {
		scope, err := m.scope(obj)
		if err != nil {
			return reconcile.Signal{}, err
		}
		return reconcile.Signal{Kind: reconcile.SignalStarted, ID: id, Ref: ref, Scope: scope}, nil
	}

	return reconcile.Signal{Kind: reconcile.SignalNone}, nil
}

// scope builds the transaction scope from the Flux inventory and target
// namespace.
func (m *Mapper) scope(obj *unstructured.Unstructured) (txn.Scope, error) {
	b := txn.NewScopeBuilder()
	entries, _, _ := unstructured.NestedSlice(obj.Object, "status", "inventory", "entries")
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		rawID, _ := em["id"].(string)
		ref, ok := parseInventoryID(rawID)
		if !ok {
			continue
		}
		resource, err := m.resolver.ResourceFor(ref.Group, ref.Kind)
		if err != nil {
			return txn.Scope{}, err
		}
		b.AddRef(txn.ResourceRef{Group: ref.Group, Resource: resource, Namespace: ref.Namespace, Name: ref.Name})
	}
	if m.nsScope == modes.NamespaceScopeAuto {
		if targetNS := releaseNamespace(obj); targetNS != "" {
			b.AddNamespace(targetNS)
		}
	}
	b.ExpandOwners(true)
	return b.Build(), nil
}

// invRef is a parsed Flux inventory entry.
type invRef struct {
	Namespace string
	Name      string
	Group     string
	Kind      string
}

// parseInventoryID parses the Flux inventory id format
// <namespace>_<name>_<group>_<kind>. A cluster-scoped object has an empty
// namespace segment (the id starts with "_"). The group is empty for core.
func parseInventoryID(id string) (invRef, bool) {
	parts := strings.Split(id, "_")
	if len(parts) != 4 {
		return invRef{}, false
	}
	r := invRef{Namespace: parts[0], Name: parts[1], Group: parts[2], Kind: parts[3]}
	if r.Name == "" || r.Kind == "" {
		return invRef{}, false
	}
	return r, true
}

// releaseNamespace returns the destination namespace: spec.targetNamespace for
// a Kustomization, or the release namespace for a HelmRelease.
func releaseNamespace(obj *unstructured.Unstructured) string {
	// Kustomization and HelmRelease both expose spec.targetNamespace.
	if ns, ok, _ := unstructured.NestedString(obj.Object, "spec", "targetNamespace"); ok && ns != "" {
		return ns
	}
	// HelmRelease records its effective namespace in status.storageNamespace.
	if ns, ok, _ := unstructured.NestedString(obj.Object, "status", "storageNamespace"); ok && ns != "" {
		return ns
	}
	return obj.GetNamespace()
}

// condition returns the status and reason of a status condition by type.
func condition(obj *unstructured.Unstructured, condType string) (status, reason string) {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == condType {
			status, _ = cm["status"].(string)
			reason, _ = cm["reason"].(string)
			return status, reason
		}
	}
	return "", ""
}

// gvrFor returns the GVR for a Flux object based on its kind, defaulting to
// the Kustomization GVR.
func gvrFor(obj *unstructured.Unstructured) schema.GroupVersionResource {
	if obj.GetKind() == "HelmRelease" {
		return HelmReleaseGVR
	}
	return KustomizationGVR
}

func isProgressing(reason string) bool {
	switch reason {
	case "Progressing", "ProgressingWithRetry", "DependencyNotReady":
		return true
	}
	return false
}
