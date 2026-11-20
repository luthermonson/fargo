// Package modes maps the objects of a deployment system (Argo CD, Flux, or
// the rewind Transaction CRD) to rollout Signals the reconcile engine drives.
// Each mapper is a pure function over an object, which makes every mode's
// begin/commit/rollback decision unit-testable on fixtures.
package modes

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/luthermonson/rewind/pkg/reconcile"
)

// NamespaceScope selects whether a destination/target namespace is added to
// the transaction scope.
type NamespaceScope string

const (
	// NamespaceScopeAuto adds the destination/target namespace to the scope.
	NamespaceScopeAuto NamespaceScope = "auto"
	// NamespaceScopeNone leaves the namespace out of the scope.
	NamespaceScopeNone NamespaceScope = "none"
)

// Mapper watches one deployment system and classifies its objects.
type Mapper interface {
	// Resources lists the GVRs the controller informs on.
	Resources() []schema.GroupVersionResource
	// Classify maps the current state of an object to a rollout Signal.
	Classify(obj *unstructured.Unstructured) (reconcile.Signal, error)
	// OnDelete handles object deletion (used by crd mode to roll back an
	// open Transaction that is deleted). The bool reports whether the Signal
	// is actionable.
	OnDelete(obj *unstructured.Unstructured) (reconcile.Signal, bool)
}

// ResourceResolver maps an API group + Kind to its plural resource name, used
// to turn a deployment system's typed object list into kine scope keys. It is
// backed by the target cluster's discovery in production and a fixed table in
// tests.
type ResourceResolver interface {
	ResourceFor(group, kind string) (string, error)
}

// NoDelete is embedded by mappers that do not react to deletes.
type NoDelete struct{}

// OnDelete reports no actionable signal.
func (NoDelete) OnDelete(*unstructured.Unstructured) (reconcile.Signal, bool) {
	return reconcile.Signal{}, false
}
