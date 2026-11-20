package txn

import (
	"sort"
	"strings"
)

// Scope is the set of keys a transaction may revert. It mirrors the kine
// control API v0 scope object exactly: a key is in scope if it equals an
// entry of Keys, starts with an entry of Prefixes, lives in one of
// Namespaces, or (with ExpandOwners) its owner chain reaches an in-scope key
// at rollback time.
type Scope struct {
	Keys         []string `json:"keys,omitempty"`
	Prefixes     []string `json:"prefixes,omitempty"`
	Namespaces   []string `json:"namespaces,omitempty"`
	ExpandOwners bool     `json:"expandOwners,omitempty"`
}

// ResourceRef identifies a single Kubernetes object by API group, plural
// resource, namespace and name. An empty Group means the core group.
type ResourceRef struct {
	Group     string
	Resource  string
	Namespace string
	Name      string
}

// Key renders a ResourceRef as a kine scope key in the form the API accepts:
// group/resource/namespace/name, or resource/namespace/name for the core
// group. kine maps these to etcd keys. A cluster-scoped object (no namespace)
// is rendered as .../name with the namespace segment omitted.
func (r ResourceRef) Key() string {
	parts := make([]string, 0, 4)
	if r.Group != "" {
		parts = append(parts, r.Group)
	}
	parts = append(parts, r.Resource)
	if r.Namespace != "" {
		parts = append(parts, r.Namespace)
	}
	parts = append(parts, r.Name)
	return strings.Join(parts, "/")
}

// ScopeBuilder accumulates resource refs, namespace prefixes and flags, then
// produces a de-duplicated, stable Scope. It is the one place that turns a
// deployment system's object list into the API's scope shape.
type ScopeBuilder struct {
	keys         map[string]struct{}
	prefixes     map[string]struct{}
	namespaces   map[string]struct{}
	expandOwners bool
}

// NewScopeBuilder returns an empty ScopeBuilder.
func NewScopeBuilder() *ScopeBuilder {
	return &ScopeBuilder{
		keys:       map[string]struct{}{},
		prefixes:   map[string]struct{}{},
		namespaces: map[string]struct{}{},
	}
}

// AddRef adds a single object to the scope by its group/resource/namespace/name.
// Empty refs (no resource or no name) are ignored so callers can map loosely.
func (b *ScopeBuilder) AddRef(r ResourceRef) *ScopeBuilder {
	if r.Resource == "" || r.Name == "" {
		return b
	}
	b.keys[r.Key()] = struct{}{}
	return b
}

// AddKey adds a pre-rendered scope key.
func (b *ScopeBuilder) AddKey(k string) *ScopeBuilder {
	if k = strings.TrimSpace(k); k != "" {
		b.keys[k] = struct{}{}
	}
	return b
}

// AddPrefix adds a key prefix; every key starting with it is in scope.
func (b *ScopeBuilder) AddPrefix(p string) *ScopeBuilder {
	if p = strings.TrimSpace(p); p != "" {
		b.prefixes[p] = struct{}{}
	}
	return b
}

// AddNamespace puts an entire namespace in scope.
func (b *ScopeBuilder) AddNamespace(ns string) *ScopeBuilder {
	if ns = strings.TrimSpace(ns); ns != "" {
		b.namespaces[ns] = struct{}{}
	}
	return b
}

// ExpandOwners sets the expandOwners flag.
func (b *ScopeBuilder) ExpandOwners(v bool) *ScopeBuilder {
	b.expandOwners = v
	return b
}

// Build returns the accumulated Scope with sorted, de-duplicated members.
func (b *ScopeBuilder) Build() Scope {
	return Scope{
		Keys:         sortedKeys(b.keys),
		Prefixes:     sortedKeys(b.prefixes),
		Namespaces:   sortedKeys(b.namespaces),
		ExpandOwners: b.expandOwners,
	}
}

// Empty reports whether the scope would select nothing.
func (b *ScopeBuilder) Empty() bool {
	return len(b.keys) == 0 && len(b.prefixes) == 0 && len(b.namespaces) == 0
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
