package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/config"
	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/controller"
	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/k8sclient"
	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/leader"
	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/metrics"
)

const metricsAddr = ":8080"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	clientset, err := k8sclient.New()
	if err != nil {
		log.Fatalf("failed to create clientset: %v", err)
	}

	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: clientset.CoreV1().Events("")})
	defer broadcaster.Shutdown()
	recorder := broadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "aegis-controller"})

	go metrics.Serve(ctx, metricsAddr)

	log.Printf("Aegis controller started, watching pods in %s (max restarts %d, pending timeout %v, max %d deletions/min)",
		cfg.Namespace, cfg.Policy.MaxRestarts, cfg.Policy.PendingTimeout, cfg.MaxDeletionsPerMinute)

	c := controller.New(clientset, recorder, cfg, controller.Counters{
		CrashLoop: metrics.CrashLoopDeletions,
		Pending:   metrics.PendingDeletions,
		Throttled: metrics.Throttled,
	})

	if cfg.LeaderElection {
		log.Printf("leader election enabled, competing for Lease %s/%s as %s",
			cfg.LeaseNamespace, leader.LeaseName, cfg.Identity)
		err = leader.Run(ctx, clientset, cfg.LeaseNamespace, cfg.Identity,
			leader.DefaultTimings, metrics.IsLeader, c.Run)
	} else {
		metrics.IsLeader.Set(1)
		err = c.Run(ctx)
	}
	if err != nil {
		log.Fatalf("controller stopped: %v", err)
	}
	log.Println("Aegis controller shut down")
}
