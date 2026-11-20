package controller

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/restmapper"
)

// discoveryResolver maps an API group + Kind to its plural resource using the
// target cluster's discovery. It satisfies modes.ResourceResolver.
type discoveryResolver struct {
	mapper meta.RESTMapper
}

// newDiscoveryResolver builds a resolver from a discovery client.
func newDiscoveryResolver(dc discovery.DiscoveryInterface) (*discoveryResolver, error) {
	groups, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, fmt.Errorf("discover API resources on target cluster: %w", err)
	}
	return &discoveryResolver{mapper: restmapper.NewDiscoveryRESTMapper(groups)}, nil
}

// ResourceFor returns the plural resource for a group+kind.
func (r *discoveryResolver) ResourceFor(group, kind string) (string, error) {
	m, err := r.mapper.RESTMapping(schema.GroupKind{Group: group, Kind: kind})
	if err != nil {
		return "", fmt.Errorf("map %s/%s to a resource: %w", group, kind, err)
	}
	return m.Resource.Resource, nil
}

// staticResolver is a fallback table for the common core/workload kinds, used
// when no target cluster is configured. It lets argo/flux scope objects by the
// usual kinds without discovery.
type staticResolver struct{}

var staticTable = map[string]string{
	"Deployment":              "deployments",
	"ReplicaSet":              "replicasets",
	"StatefulSet":             "statefulsets",
	"DaemonSet":               "daemonsets",
	"Pod":                     "pods",
	"Service":                 "services",
	"ConfigMap":               "configmaps",
	"Secret":                  "secrets",
	"ServiceAccount":          "serviceaccounts",
	"Ingress":                 "ingresses",
	"Job":                     "jobs",
	"CronJob":                 "cronjobs",
	"PersistentVolumeClaim":   "persistentvolumeclaims",
	"Role":                    "roles",
	"RoleBinding":             "rolebindings",
	"ClusterRole":             "clusterroles",
	"ClusterRoleBinding":      "clusterrolebindings",
	"HorizontalPodAutoscaler": "horizontalpodautoscalers",
}

// ResourceFor returns the plural resource, falling back to a lowercase-plural
// guess for kinds not in the table.
func (staticResolver) ResourceFor(_, kind string) (string, error) {
	if r, ok := staticTable[kind]; ok {
		return r, nil
	}
	if kind == "" {
		return "", fmt.Errorf("empty kind")
	}
	// Last-resort pluralization: lowercases the kind and adds "s".
	lower := strings.ToLower(kind)
	if strings.HasSuffix(lower, "s") {
		return lower + "es", nil
	}
	if strings.HasSuffix(lower, "y") {
		return lower[:len(lower)-1] + "ies", nil
	}
	return lower + "s", nil
}
