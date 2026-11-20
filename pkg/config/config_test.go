package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/luthermonson/rewind/pkg/modes"
)

func TestValidate(t *testing.T) {
	base := func() Config {
		return Config{Mode: ModeArgo, KineEndpoint: "http://k:2381", HealthDeadline: time.Minute, NamespaceScope: modes.NamespaceScopeAuto}
	}
	good := base()
	if err := good.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := base()
	bad.Mode = "nope"
	if err := bad.Validate(); err == nil {
		t.Error("invalid mode accepted")
	}
	noEP := base()
	noEP.KineEndpoint = ""
	if err := noEP.Validate(); err == nil {
		t.Error("missing kine endpoint accepted")
	}
	defNS := base()
	defNS.NamespaceScope = ""
	if err := defNS.Validate(); err != nil || defNS.NamespaceScope != modes.NamespaceScopeAuto {
		t.Errorf("namespace-scope default not applied: %v %q", err, defNS.NamespaceScope)
	}
}

func TestNeedsTargetCluster(t *testing.T) {
	if !(Config{Mode: ModeArgo}).NeedsTargetCluster() || !(Config{Mode: ModeFlux}).NeedsTargetCluster() {
		t.Error("argo/flux need the target cluster")
	}
	if (Config{Mode: ModeCRD}).NeedsTargetCluster() {
		t.Error("crd does not need the target cluster")
	}
}

func TestKineToken(t *testing.T) {
	c := Config{}
	if tok, err := c.KineToken(); err != nil || tok != "" {
		t.Fatalf("empty token file should yield empty token: %q %v", tok, err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "token")
	if err := os.WriteFile(p, []byte("  s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.KineTokenFile = p
	tok, err := c.KineToken()
	if err != nil || tok != "s3cret" {
		t.Fatalf("token = %q err = %v", tok, err)
	}
}
