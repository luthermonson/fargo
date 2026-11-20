// Package argo maps Argo CD Applications to rollout Signals.
//
// Begin: status.operationState.phase == Running with operation.sync set.
// Scope: status.resources[] (group, kind -> resource via target discovery,
// namespace, name) plus spec.destination.namespace when namespace scope is
// auto. The transaction id uses status.operationState.operation.sync.revision.
// Commit: phase == Succeeded followed by status.health.status == Healthy.
// Rollback: phase Failed/Error, or health Degraded (deadline is the engine's).
package argo

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/luthermonson/rewind/pkg/modes"
	"github.com/luthermonson/rewind/pkg/reconcile"
	"github.com/luthermonson/rewind/pkg/txn"
)

// GVR is the Application resource rewind watches.
var GVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// Mapper implements modes.Mapper for Argo CD.
type Mapper struct {
	modes.NoDelete
	resolver modes.ResourceResolver
	nsScope  modes.NamespaceScope
}

// New builds an Argo mapper. resolver maps kind->resource using the target
// cluster's discovery; nsScope controls destination-namespace inclusion.
func New(resolver modes.ResourceResolver, nsScope modes.NamespaceScope) *Mapper {
	return &Mapper{resolver: resolver, nsScope: nsScope}
}

// Resources returns the Application GVR.
func (m *Mapper) Resources() []schema.GroupVersionResource {
	return []schema.GroupVersionResource{GVR}
}

// Classify maps an Application to a rollout Signal.
func (m *Mapper) Classify(obj *unstructured.Unstructured) (reconcile.Signal, error) {
	name := obj.GetName()
	ns := obj.GetNamespace()
	ref := reconcile.ObjRef{
		Namespace:   ns,
		Name:        name,
		GVRGroup:    GVR.Group,
		GVRVersion:  GVR.Version,
		GVRResource: GVR.Resource,
	}

	phase, _, _ := unstructured.NestedString(obj.Object, "status", "operationState", "phase")
	revision := syncRevision(obj)
	id := reconcile.SanitizeID("argo", ns, name, revision)

	switch phase {
	case "Running":
		if !hasSyncOperation(obj) {
			return reconcile.Signal{Kind: reconcile.SignalNone}, nil
		}
		scope, err := m.scope(obj)
		if err != nil {
			return reconcile.Signal{}, err
		}
		return reconcile.Signal{Kind: reconcile.SignalStarted, ID: id, Ref: ref, Scope: scope}, nil
	case "Succeeded":
		health, _, _ := unstructured.NestedString(obj.Object, "status", "health", "status")
		switch health {
		case "Healthy":
			return reconcile.Signal{Kind: reconcile.SignalSucceeded, ID: id, Ref: ref}, nil
		case "Degraded":
			return reconcile.Signal{Kind: reconcile.SignalFailed, ID: id, Ref: ref, Reason: "health Degraded after sync"}, nil
		default:
			// Synced but not yet Healthy: wait (engine deadline decides).
			return reconcile.Signal{Kind: reconcile.SignalNone}, nil
		}
	case "Failed", "Error":
		return reconcile.Signal{Kind: reconcile.SignalFailed, ID: id, Ref: ref, Reason: "operation phase " + phase}, nil
	default:
		return reconcile.Signal{Kind: reconcile.SignalNone}, nil
	}
}

// scope builds the transaction scope from status.resources and the
// destination namespace.
func (m *Mapper) scope(obj *unstructured.Unstructured) (txn.Scope, error) {
	b := txn.NewScopeBuilder()
	resources, _, _ := unstructured.NestedSlice(obj.Object, "status", "resources")
	for _, r := range resources {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		group, _ := rm["group"].(string)
		kind, _ := rm["kind"].(string)
		namespace, _ := rm["namespace"].(string)
		rName, _ := rm["name"].(string)
		if kind == "" || rName == "" {
			continue
		}
		resource, err := m.resolver.ResourceFor(group, kind)
		if err != nil {
			return txn.Scope{}, err
		}
		b.AddRef(txn.ResourceRef{Group: group, Resource: resource, Namespace: namespace, Name: rName})
	}
	if m.nsScope == modes.NamespaceScopeAuto {
		if destNS, _, _ := unstructured.NestedString(obj.Object, "spec", "destination", "namespace"); destNS != "" {
			b.AddNamespace(destNS)
		}
	}
	// Owner expansion catches controller-created children (ReplicaSets, Pods)
	// whose own keys are not in the Application's resource list.
	b.ExpandOwners(true)
	return b.Build(), nil
}

func syncRevision(obj *unstructured.Unstructured) string {
	if rev, ok, _ := unstructured.NestedString(obj.Object, "status", "operationState", "operation", "sync", "revision"); ok && rev != "" {
		return rev
	}
	// Fall back to the synced revision Argo records.
	if rev, ok, _ := unstructured.NestedString(obj.Object, "status", "sync", "revision"); ok {
		return rev
	}
	return ""
}

func hasSyncOperation(obj *unstructured.Unstructured) bool {
	if _, ok, _ := unstructured.NestedMap(obj.Object, "status", "operationState", "operation", "sync"); ok {
		return true
	}
	_, ok, _ := unstructured.NestedMap(obj.Object, "operation", "sync")
	return ok
}
