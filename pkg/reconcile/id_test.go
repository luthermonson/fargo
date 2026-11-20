package reconcile

import "testing"

func TestSanitizeID(t *testing.T) {
	cases := []struct {
		mode, ns, name, rev, want string
	}{
		{"argo", "argocd", "podinfo", "abc123", "argo/argocd/podinfo/abc123"},
		{"flux", "flux-system", "podinfo", "main@sha1:abcdef", "flux/flux-system/podinfo/main-sha1-abcdef"},
		{"crd", "ci", "deploy/podinfo", "1", "crd/ci/deploy-podinfo/1"},
		{"argo", "", "x", "", "argo/_/x/_"},
	}
	for _, c := range cases {
		if got := SanitizeID(c.mode, c.ns, c.name, c.rev); got != c.want {
			t.Errorf("SanitizeID(%q,%q,%q,%q) = %q, want %q", c.mode, c.ns, c.name, c.rev, got, c.want)
		}
	}
}
