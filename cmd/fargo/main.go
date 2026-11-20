// Command fargo is a Kubernetes controller that makes deployments
// transactional on a k3s-on-beads cluster. It watches a deployment system
// (Argo CD, Flux, or the fargo Transaction CRD), opens a scoped transaction
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

	"github.com/luthermonson/fargo/pkg/config"
	"github.com/luthermonson/fargo/pkg/controller"
	"github.com/luthermonson/fargo/pkg/modes"
	"github.com/luthermonson/fargo/pkg/version"
)

func main() {
	app := &cli.App{
		Name:    "fargo",
		Usage:   "transactional deployments on a k3s-on-beads cluster",
		Version: version.Version,
		Description: "fargo watches a deployment system and brackets each rollout in a\n" +
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
		&cli.StringFlag{Name: "mode", Usage: "deployment system to watch: argo|flux|crd", EnvVars: []string{"FARGO_MODE"}, Required: true},
		&cli.StringFlag{Name: "management-kubeconfig", Usage: "kubeconfig where Applications/Kustomizations/HelmReleases/Transactions are watched (default: in-cluster)", EnvVars: []string{"FARGO_MANAGEMENT_KUBECONFIG"}},
		&cli.StringFlag{Name: "management-context", Usage: "context to use in the management kubeconfig", EnvVars: []string{"FARGO_MANAGEMENT_CONTEXT"}},
		&cli.StringFlag{Name: "target-kubeconfig", Usage: "kubeconfig of the managed cluster, for discovery and health (default: in-cluster)", EnvVars: []string{"FARGO_TARGET_KUBECONFIG"}},
		&cli.StringFlag{Name: "kine-endpoint", Usage: "kine control API endpoint: http://host:port or unix:///path/kine.sock", EnvVars: []string{"FARGO_KINE_ENDPOINT"}},
		&cli.StringFlag{Name: "kine-token-file", Usage: "file holding the kine bearer token (mounted from a Secret)", EnvVars: []string{"FARGO_KINE_TOKEN_FILE"}},
		&cli.DurationFlag{Name: "health-deadline", Usage: "how long to wait for a rollout to become healthy before rolling back", Value: 5 * time.Minute, EnvVars: []string{"FARGO_HEALTH_DEADLINE"}},
		&cli.StringFlag{Name: "namespace-scope", Usage: "auto|none: whether to add the destination namespace to the transaction scope", Value: "auto", EnvVars: []string{"FARGO_NAMESPACE_SCOPE"}},
		&cli.StringFlag{Name: "namespace", Usage: "namespace to watch (empty = all) and hold the leader-election lease", EnvVars: []string{"FARGO_NAMESPACE"}},
		&cli.BoolFlag{Name: "dry-run", Usage: "log begin/commit/rollback calls without sending them", EnvVars: []string{"FARGO_DRY_RUN"}},
		&cli.StringFlag{Name: "metrics-bind-address", Usage: "address for /metrics, /healthz, /readyz (empty = off)", Value: ":8080", EnvVars: []string{"FARGO_METRICS_BIND_ADDRESS"}},
		&cli.BoolFlag{Name: "leader-elect", Usage: "run leader election so only one replica is active", EnvVars: []string{"FARGO_LEADER_ELECT"}},
		&cli.StringFlag{Name: "leader-elect-id", Usage: "name of the leader-election lease", EnvVars: []string{"FARGO_LEADER_ELECT_ID"}},
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

	log.Info("fargo starting", append([]any{"version", version.Version}, cfg.LogFields()...)...)
	return ctrl.Run(ctx)
}
