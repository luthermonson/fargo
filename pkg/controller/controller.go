// Package controller wires the rewind runtime together: Kubernetes clients for
// the management and target clusters, the kine transaction client, the mode
// mapper, dynamic informers, the reconcile engine, leader election and the
// metrics endpoint.
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/luthermonson/rewind/pkg/config"
	"github.com/luthermonson/rewind/pkg/modes"
	"github.com/luthermonson/rewind/pkg/modes/argo"
	"github.com/luthermonson/rewind/pkg/modes/crd"
	"github.com/luthermonson/rewind/pkg/modes/flux"
	"github.com/luthermonson/rewind/pkg/reconcile"
	"github.com/luthermonson/rewind/pkg/txn"
)

// Controller is an assembled, runnable rewind instance.
type Controller struct {
	cfg     config.Config
	log     *slog.Logger
	mapper  modes.Mapper
	engine  *reconcile.Engine
	mgmt    dynamic.Interface
	mgmtRC  *rest.Config
	metrics *metrics
}

// New builds a Controller from resolved configuration.
func New(cfg config.Config, log *slog.Logger) (*Controller, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	mgmtRC, err := restConfig(cfg.ManagementKubeconfig, cfg.ManagementContext)
	if err != nil {
		return nil, fmt.Errorf("management cluster config: %w", err)
	}
	mgmt, err := dynamic.NewForConfig(mgmtRC)
	if err != nil {
		return nil, fmt.Errorf("management dynamic client: %w", err)
	}

	token, err := cfg.KineToken()
	if err != nil {
		return nil, err
	}
	kineClient, err := txn.New(cfg.KineEndpoint, txn.WithToken(token))
	if err != nil {
		return nil, fmt.Errorf("kine client: %w", err)
	}

	var resolver modes.ResourceResolver = staticResolver{}
	if cfg.NeedsTargetCluster() {
		targetRC, err := restConfig(cfg.TargetKubeconfig, "")
		if err != nil {
			return nil, fmt.Errorf("target cluster config: %w", err)
		}
		dc, err := discovery.NewDiscoveryClientForConfig(targetRC)
		if err != nil {
			return nil, fmt.Errorf("target discovery client: %w", err)
		}
		if resolved, err := newDiscoveryResolver(dc); err != nil {
			log.Warn("target discovery unavailable; falling back to the static kind table", "err", err)
		} else {
			resolver = resolved
		}
	}

	var mapper modes.Mapper
	var annotator reconcile.Annotator
	switch cfg.Mode {
	case config.ModeArgo:
		mapper = argo.New(resolver, cfg.NamespaceScope)
		annotator = &annotateWriter{client: mgmt}
	case config.ModeFlux:
		mapper = flux.New(resolver, cfg.NamespaceScope)
		annotator = &annotateWriter{client: mgmt}
	case config.ModeCRD:
		mapper = crd.New()
		annotator = &statusWriter{client: mgmt}
	default:
		return nil, fmt.Errorf("unsupported mode %q", cfg.Mode)
	}

	m := &metrics{}
	engine := reconcile.NewEngine(reconcile.Config{
		Client:         kineClient,
		Annotator:      annotator,
		Observer:       m,
		HealthDeadline: cfg.HealthDeadline,
		DryRun:         cfg.DryRun,
		Logger:         log,
	})

	return &Controller{
		cfg:     cfg,
		log:     log,
		mapper:  mapper,
		engine:  engine,
		mgmt:    mgmt,
		mgmtRC:  mgmtRC,
		metrics: m,
	}, nil
}

// Run starts the metrics server and (optionally under leader election) the
// informers, blocking until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	if addr := c.cfg.MetricsBindAddress; addr != "" {
		srv, err := serveMetrics(addr, c.metrics)
		if err != nil {
			return err
		}
		c.log.Info("metrics server listening", "address", addr)
		defer func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
		}()
	}

	if !c.cfg.LeaderElect {
		return c.runInformers(ctx)
	}
	return c.runWithLeaderElection(ctx)
}

func (c *Controller) runWithLeaderElection(ctx context.Context) error {
	k8s, err := kubernetes.NewForConfig(c.mgmtRC)
	if err != nil {
		return fmt.Errorf("leader-election client: %w", err)
	}
	ns := c.cfg.Namespace
	if ns == "" {
		ns = "kube-system"
	}
	id, _ := os.Hostname()
	if id == "" {
		id = "rewind"
	}
	leaseName := c.cfg.LeaderElectID
	if leaseName == "" {
		leaseName = "rewind-" + string(c.cfg.Mode)
	}
	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: leaseName, Namespace: ns},
		Client:     k8s.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: id},
	}

	runErr := make(chan error, 1)
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		ReleaseOnCancel: true,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(c2 context.Context) {
				c.log.Info("acquired leadership; starting informers", "id", id)
				runErr <- c.runInformers(c2)
			},
			OnStoppedLeading: func() {
				c.log.Info("lost leadership", "id", id)
			},
		},
	})
	select {
	case err := <-runErr:
		return err
	default:
		return nil
	}
}

func (c *Controller) runInformers(ctx context.Context) error {
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.mgmt, 10*time.Minute, c.cfg.Namespace, nil)

	informers := map[schema.GroupVersionResource]cache.SharedIndexInformer{}
	for _, gvr := range c.mapper.Resources() {
		inf := factory.ForResource(gvr).Informer()
		_, err := inf.AddEventHandler(c.handlersFor(ctx))
		if err != nil {
			return fmt.Errorf("add informer for %s: %w", gvr, err)
		}
		informers[gvr] = inf
	}

	c.log.Info("starting informers", "mode", c.cfg.Mode, "resources", gvrNames(c.mapper.Resources()), "dryRun", c.cfg.DryRun)
	factory.Start(ctx.Done())
	synced := factory.WaitForCacheSync(ctx.Done())
	for gvr, ok := range synced {
		if !ok {
			return fmt.Errorf("informer cache for %s failed to sync", gvr)
		}
	}
	// Say what the management cluster holds, so a misconfigured kubeconfig or
	// context is obvious at startup rather than at the first rollout.
	for gvr, inf := range informers {
		c.log.Info("informer synced", "resource", gvr.String(), "objects", len(inf.GetStore().ListKeys()))
	}
	<-ctx.Done()
	return nil
}

func (c *Controller) handlersFor(ctx context.Context) cache.ResourceEventHandlerFuncs {
	classify := func(obj any) {
		u, ok := toUnstructured(obj)
		if !ok {
			return
		}
		sig, err := c.mapper.Classify(u)
		if err != nil {
			c.log.Error("classify failed", "name", u.GetName(), "err", err)
			c.metrics.Errored(u.GetName())
			return
		}
		if sig.Kind == reconcile.SignalNone {
			return
		}
		if err := c.engine.Handle(ctx, sig); err != nil {
			c.log.Error("reconcile failed", "id", sig.ID, "err", err)
			c.metrics.Errored(sig.ID)
		}
	}
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    classify,
		UpdateFunc: func(_, newObj any) { classify(newObj) },
		DeleteFunc: func(obj any) {
			u, ok := toUnstructured(obj)
			if !ok {
				return
			}
			sig, actionable := c.mapper.OnDelete(u)
			if !actionable {
				return
			}
			if err := c.engine.Handle(ctx, sig); err != nil {
				c.log.Error("delete reconcile failed", "id", sig.ID, "err", err)
				c.metrics.Errored(sig.ID)
			}
		},
	}
}

func toUnstructured(obj any) (*unstructured.Unstructured, bool) {
	switch t := obj.(type) {
	case *unstructured.Unstructured:
		return t, true
	case cache.DeletedFinalStateUnknown:
		if u, ok := t.Obj.(*unstructured.Unstructured); ok {
			return u, true
		}
	}
	return nil, false
}

// restConfig loads a rest.Config from a kubeconfig file (+ optional context),
// or falls back to in-cluster config when no path is given.
func restConfig(kubeconfig, kubecontext string) (*rest.Config, error) {
	if kubeconfig == "" {
		if rc, err := rest.InClusterConfig(); err == nil {
			return rc, nil
		}
		// Fall back to the default loading rules (KUBECONFIG / ~/.kube/config).
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if kubecontext != "" {
		overrides.CurrentContext = kubecontext
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

func gvrNames(gvrs []schema.GroupVersionResource) []string {
	out := make([]string, 0, len(gvrs))
	for _, g := range gvrs {
		out = append(out, g.String())
	}
	return out
}
