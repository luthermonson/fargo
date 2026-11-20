// Package crd maps the fargo Transaction CRD
// (transactions.fargo.gascity.dev/v1alpha1) to rollout Signals. It is the
// airtight mode: there is no deployment system racing the controller, so the
// savepoint is taken before anything is applied.
//
// Create (no outcome, phase empty/Pending) -> begin.
// spec.outcome == commit -> commit; spec.outcome == rollback -> rollback.
// Delete while Open -> rollback.
package crd

import (
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/luthermonson/fargo/pkg/reconcile"
	"github.com/luthermonson/fargo/pkg/txn"
)

// Group/version/resource for the Transaction CRD.
const (
	Group    = "fargo.gascity.dev"
	Version  = "v1alpha1"
	Resource = "transactions"
	Kind     = "Transaction"
)

// GVR is the Transaction resource fargo watches.
var GVR = schema.GroupVersionResource{Group: Group, Version: Version, Resource: Resource}

// Mapper implements modes.Mapper for the Transaction CRD.
type Mapper struct{}

// New builds a CRD mapper.
func New() *Mapper { return &Mapper{} }

// Resources returns the Transaction GVR.
func (m *Mapper) Resources() []schema.GroupVersionResource {
	return []schema.GroupVersionResource{GVR}
}

// id builds a stable transaction id for a Transaction object. The revision
// segment is the object generation so re-applying the same spec is idempotent.
func id(obj *unstructured.Unstructured) string {
	rev := strconv.FormatInt(obj.GetGeneration(), 10)
	return reconcile.SanitizeID("crd", obj.GetNamespace(), obj.GetName(), rev)
}

// Classify maps a Transaction to a rollout Signal.
func (m *Mapper) Classify(obj *unstructured.Unstructured) (reconcile.Signal, error) {
	ref := objRef(obj)
	tid := id(obj)
	outcome, _, _ := unstructured.NestedString(obj.Object, "spec", "outcome")
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")

	switch outcome {
	case "commit":
		return reconcile.Signal{Kind: reconcile.SignalSucceeded, ID: tid, Ref: ref}, nil
	case "rollback":
		return reconcile.Signal{Kind: reconcile.SignalFailed, ID: tid, Ref: ref, Reason: "spec.outcome=rollback"}, nil
	}

	// No outcome yet: begin unless already finished.
	switch phase {
	case "Committed", "RolledBack", "Failed":
		return reconcile.Signal{Kind: reconcile.SignalNone}, nil
	}
	scope, err := parseScope(obj)
	if err != nil {
		return reconcile.Signal{}, err
	}
	return reconcile.Signal{Kind: reconcile.SignalStarted, ID: tid, Ref: ref, Scope: scope}, nil
}

// OnDelete rolls back an open Transaction that is deleted.
func (m *Mapper) OnDelete(obj *unstructured.Unstructured) (reconcile.Signal, bool) {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	if phase != "Open" && phase != "Pending" {
		return reconcile.Signal{}, false
	}
	return reconcile.Signal{Kind: reconcile.SignalFailed, ID: id(obj), Ref: objRef(obj), Reason: "Transaction deleted while open"}, true
}

func objRef(obj *unstructured.Unstructured) reconcile.ObjRef {
	return reconcile.ObjRef{
		Namespace:   obj.GetNamespace(),
		Name:        obj.GetName(),
		GVRGroup:    GVR.Group,
		GVRVersion:  GVR.Version,
		GVRResource: GVR.Resource,
	}
}

// parseScope reads spec.scope, which has the same shape as the API scope.
func parseScope(obj *unstructured.Unstructured) (txn.Scope, error) {
	b := txn.NewScopeBuilder()
	keys, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "scope", "keys")
	for _, k := range keys {
		b.AddKey(k)
	}
	prefixes, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "scope", "prefixes")
	for _, p := range prefixes {
		b.AddPrefix(p)
	}
	namespaces, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "scope", "namespaces")
	for _, n := range namespaces {
		b.AddNamespace(n)
	}
	expand, _, _ := unstructured.NestedBool(obj.Object, "spec", "scope", "expandOwners")
	b.ExpandOwners(expand)
	return b.Build(), nil
}
