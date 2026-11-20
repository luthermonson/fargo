package txn_test

import (
	"reflect"
	"testing"

	"github.com/luthermonson/fargo/pkg/txn"
)

func TestResourceRefKey(t *testing.T) {
	cases := []struct {
		ref  txn.ResourceRef
		want string
	}{
		{txn.ResourceRef{Group: "apps", Resource: "deployments", Namespace: "web", Name: "podinfo"}, "apps/deployments/web/podinfo"},
		{txn.ResourceRef{Resource: "configmaps", Namespace: "web", Name: "cfg"}, "configmaps/web/cfg"},
		{txn.ResourceRef{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "admin"}, "rbac.authorization.k8s.io/clusterroles/admin"},
	}
	for _, c := range cases {
		if got := c.ref.Key(); got != c.want {
			t.Errorf("Key(%+v) = %q, want %q", c.ref, got, c.want)
		}
	}
}

func TestScopeBuilderDedupeAndSort(t *testing.T) {
	b := txn.NewScopeBuilder()
	b.AddRef(txn.ResourceRef{Group: "apps", Resource: "deployments", Namespace: "web", Name: "podinfo"})
	b.AddRef(txn.ResourceRef{Group: "apps", Resource: "deployments", Namespace: "web", Name: "podinfo"}) // dup
	b.AddRef(txn.ResourceRef{Resource: "services", Namespace: "web", Name: "podinfo"})
	b.AddRef(txn.ResourceRef{Resource: "", Name: "ignored"}) // skipped
	b.AddNamespace("web")
	b.AddPrefix("/registry/apps/deployments/web/")
	b.ExpandOwners(true)
	got := b.Build()

	want := txn.Scope{
		Keys:         []string{"apps/deployments/web/podinfo", "services/web/podinfo"},
		Prefixes:     []string{"/registry/apps/deployments/web/"},
		Namespaces:   []string{"web"},
		ExpandOwners: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Build() = %+v\nwant %+v", got, want)
	}
}

func TestScopeBuilderEmpty(t *testing.T) {
	b := txn.NewScopeBuilder()
	if !b.Empty() {
		t.Fatal("new builder should be empty")
	}
	b.AddNamespace("x")
	if b.Empty() {
		t.Fatal("builder with a namespace is not empty")
	}
}
