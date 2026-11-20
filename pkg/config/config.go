// Package config holds the resolved runtime configuration for fargo. Flags,
// FARGO_* environment variables and Helm values all map onto this one struct,
// so changing the management cluster or the kine address is a values change
// plus a rollout, never a code change.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/luthermonson/fargo/pkg/modes"
)

// Mode selects which deployment system fargo watches.
type Mode string

const (
	ModeArgo Mode = "argo"
	ModeFlux Mode = "flux"
	ModeCRD  Mode = "crd"
)

// Config is the full runtime configuration.
type Config struct {
	Mode Mode

	ManagementKubeconfig string
	ManagementContext    string
	TargetKubeconfig     string

	KineEndpoint  string
	KineTokenFile string

	HealthDeadline     time.Duration
	NamespaceScope     modes.NamespaceScope
	DryRun             bool
	MetricsBindAddress string
	LeaderElect        bool
	LeaderElectID      string
	Namespace          string // namespace to watch (empty = all), and for the leader-election lease
}

// KineToken reads the bearer token from KineTokenFile at run time. It is never
// defaulted to a real value: the token is mounted from a Secret. An empty
// KineTokenFile means no token (unix socket / local use).
func (c *Config) KineToken() (string, error) {
	if strings.TrimSpace(c.KineTokenFile) == "" {
		return "", nil
	}
	b, err := os.ReadFile(c.KineTokenFile)
	if err != nil {
		return "", fmt.Errorf("read kine token file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Validate checks the configuration is internally consistent.
func (c *Config) Validate() error {
	switch c.Mode {
	case ModeArgo, ModeFlux, ModeCRD:
	case "":
		return fmt.Errorf("--mode is required (argo|flux|crd)")
	default:
		return fmt.Errorf("invalid --mode %q (want argo|flux|crd)", c.Mode)
	}
	switch c.NamespaceScope {
	case modes.NamespaceScopeAuto, modes.NamespaceScopeNone:
	case "":
		c.NamespaceScope = modes.NamespaceScopeAuto
	default:
		return fmt.Errorf("invalid --namespace-scope %q (want auto|none)", c.NamespaceScope)
	}
	if strings.TrimSpace(c.KineEndpoint) == "" {
		return fmt.Errorf("--kine-endpoint is required")
	}
	if c.HealthDeadline <= 0 {
		return fmt.Errorf("--health-deadline must be positive")
	}
	return nil
}

// LogFields renders the effective configuration as slog key/value pairs for
// the startup log. Paths are shown, the token itself never is: the token file
// appears as "(set)" / "(none)".
func (c *Config) LogFields() []any {
	tok := "(none)"
	if strings.TrimSpace(c.KineTokenFile) != "" {
		tok = "(set: " + c.KineTokenFile + ")"
	}
	mgmt := c.ManagementKubeconfig
	if mgmt == "" {
		mgmt = "(in-cluster)"
	}
	target := c.TargetKubeconfig
	if target == "" {
		target = "(in-cluster)"
	}
	ns := c.Namespace
	if ns == "" {
		ns = "(all)"
	}
	return []any{
		"mode", string(c.Mode),
		"managementKubeconfig", mgmt,
		"managementContext", c.ManagementContext,
		"targetKubeconfig", target,
		"kineEndpoint", c.KineEndpoint,
		"kineToken", tok,
		"healthDeadline", c.HealthDeadline.String(),
		"namespaceScope", string(c.NamespaceScope),
		"namespace", ns,
		"dryRun", c.DryRun,
		"leaderElect", c.LeaderElect,
	}
}

// NeedsTargetCluster reports whether this mode must talk to the target cluster
// (for kind->resource discovery). crd scopes are already fully-formed keys.
func (c Config) NeedsTargetCluster() bool {
	return c.Mode == ModeArgo || c.Mode == ModeFlux
}
