// Package metrics exposes basic Prometheus counters for pods the
// controller has remediated.
package metrics

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	CrashLoopDeletions = promauto.NewCounter(prometheus.CounterOpts{
		Name: "aegis_crashloop_deletions_total",
		Help: "Total number of pods deleted for being stuck in CrashLoopBackOff.",
	})

	PendingDeletions = promauto.NewCounter(prometheus.CounterOpts{
		Name: "aegis_pending_deletions_total",
		Help: "Total number of pods deleted for being stuck Pending.",
	})

	Throttled = promauto.NewCounter(prometheus.CounterOpts{
		Name: "aegis_remediations_throttled_total",
		Help: "Remediations skipped because AEGIS_MAX_DELETIONS_PER_MINUTE was reached; they are retried later.",
	})

	IsLeader = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "aegis_leader",
		Help: "1 if this replica is the active (remediating) controller, 0 if it's on standby.",
	})
)

// Handler returns the mux serving /metrics and the /healthz probe endpoint.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// Serve starts an HTTP server exposing Handler on addr, and shuts it down
// when ctx is cancelled. It blocks, so callers should run it in its own
// goroutine.
func Serve(ctx context.Context, addr string) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("metrics server listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("metrics server error: %v", err)
	}
}
