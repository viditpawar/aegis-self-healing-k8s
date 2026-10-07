package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/controller"
	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/k8sclient"
	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/metrics"
)

const (
	targetNamespace = "aegis-workloads"
	metricsAddr     = ":8080"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	clientset, err := k8sclient.New()
	if err != nil {
		log.Fatalf("failed to create clientset: %v", err)
	}

	go metrics.Serve(ctx, metricsAddr)

	log.Println("Aegis controller started, watching pods in", targetNamespace)

	c := controller.New(clientset, targetNamespace, controller.Counters{
		CrashLoop: metrics.CrashLoopDeletions,
		Pending:   metrics.PendingDeletions,
	})
	if err := c.Run(ctx); err != nil {
		log.Fatalf("controller stopped: %v", err)
	}
	log.Println("Aegis controller shut down")
}
