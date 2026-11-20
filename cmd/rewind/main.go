// Command rewind is a Kubernetes controller that makes deployments
// transactional on a k3s-on-beads cluster. It watches a deployment system
// (Argo CD, Flux, or the rewind Transaction CRD), opens a scoped transaction
// in the kine datastore when a rollout starts, commits it when the rollout is
// healthy, and rolls the cluster back when it fails.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/luthermonson/rewind/pkg/config"
	"github.com/luthermonson/rewind/pkg/controller"
	"github.com/luthermonson/rewind/pkg/modes"
	"github.com/luthermonson/rewind/pkg/version"
)

func main() {
	app := &cli.App{
		Name:    "rewind",
		Usage:   "transactional deployments on a k3s-on-beads cluster",
		Version: version.Version,
		Description: "rewind watches a deployment system and brackets each rollout in a\n" +
			"scoped kine transaction: begin on start, commit on healthy, roll back\n" +
			"on failure or health-deadline. Modes: argo, flux, crd.",
		Flags:  flags(),
		Action: run,
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "mode", Usage: "deployment system to watch: argo|flux|crd", EnvVars: []string{"REWIND_MODE"}, Required: true},
		&cli.StringFlag{Name: "management-kubeconfig", Usage: "kubeconfig where Applications/Kustomizations/HelmReleases/Transactions are watched (default: in-cluster)", EnvVars: []string{"REWIND_MANAGEMENT_KUBECONFIG"}},
		&cli.StringFlag{Name: "management-context", Usage: "context to use in the management kubeconfig", EnvVars: []string{"REWIND_MANAGEMENT_CONTEXT"}},
		&cli.StringFlag{Name: "target-kubeconfig", Usage: "kubeconfig of the managed cluster, for discovery and health (default: in-cluster)", EnvVars: []string{"REWIND_TARGET_KUBECONFIG"}},
		&cli.StringFlag{Name: "kine-endpoint", Usage: "kine control API endpoint: http://host:port or unix:///path/kine.sock", EnvVars: []string{"REWIND_KINE_ENDPOINT"}},
		&cli.StringFlag{Name: "kine-token-file", Usage: "file holding the kine bearer token (mounted from a Secret)", EnvVars: []string{"REWIND_KINE_TOKEN_FILE"}},
		&cli.DurationFlag{Name: "health-deadline", Usage: "how long to wait for a rollout to become healthy before rolling back", Value: 5 * time.Minute, EnvVars: []string{"REWIND_HEALTH_DEADLINE"}},
		&cli.StringFlag{Name: "namespace-scope", Usage: "auto|none: whether to add the destination namespace to the transaction scope", Value: "auto", EnvVars: []string{"REWIND_NAMESPACE_SCOPE"}},
		&cli.StringFlag{Name: "namespace", Usage: "namespace to watch (empty = all) and hold the leader-election lease", EnvVars: []string{"REWIND_NAMESPACE"}},
		&cli.BoolFlag{Name: "dry-run", Usage: "log begin/commit/rollback calls without sending them", EnvVars: []string{"REWIND_DRY_RUN"}},
		&cli.StringFlag{Name: "metrics-bind-address", Usage: "address for /metrics, /healthz, /readyz (empty = off)", Value: ":8080", EnvVars: []string{"REWIND_METRICS_BIND_ADDRESS"}},
		&cli.BoolFlag{Name: "leader-elect", Usage: "run leader election so only one replica is active", EnvVars: []string{"REWIND_LEADER_ELECT"}},
		&cli.StringFlag{Name: "leader-elect-id", Usage: "name of the leader-election lease", EnvVars: []string{"REWIND_LEADER_ELECT_ID"}},
	}
}

func run(c *cli.Context) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg := config.Config{
		Mode:                 config.Mode(c.String("mode")),
		ManagementKubeconfig: c.String("management-kubeconfig"),
		ManagementContext:    c.String("management-context"),
		TargetKubeconfig:     c.String("target-kubeconfig"),
		KineEndpoint:         c.String("kine-endpoint"),
		KineTokenFile:        c.String("kine-token-file"),
		HealthDeadline:       c.Duration("health-deadline"),
		NamespaceScope:       modes.NamespaceScope(c.String("namespace-scope")),
		DryRun:               c.Bool("dry-run"),
		MetricsBindAddress:   c.String("metrics-bind-address"),
		LeaderElect:          c.Bool("leader-elect"),
		LeaderElectID:        c.String("leader-elect-id"),
		Namespace:            c.String("namespace"),
	}

	ctrl, err := controller.New(cfg, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("rewind starting", "version", version.Version, "mode", cfg.Mode, "kineEndpoint", cfg.KineEndpoint, "dryRun", cfg.DryRun)
	return ctrl.Run(ctx)
}
