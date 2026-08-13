// Package metrics exposes basic Prometheus counters for pods the
// controller has remediated.
package metrics

import (
	"log"
	"net/http"

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
)

// Serve starts an HTTP server exposing the /metrics endpoint on addr. It
// blocks, so callers should run it in its own goroutine.
func Serve(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	log.Printf("metrics server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("metrics server error: %v", err)
	}
}
