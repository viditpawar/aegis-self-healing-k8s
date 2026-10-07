package leader

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

const ns = "aegis-system"

var fastTimings = Timings{
	LeaseDuration: time.Second,
	RenewDeadline: 500 * time.Millisecond,
	RetryPeriod:   100 * time.Millisecond,
}

func newGauge() prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_leader"})
}

// candidate starts Run in the background and reports when it starts
// leading and what Run eventually returned.
type candidate struct {
	gauge   prometheus.Gauge
	cancel  context.CancelFunc
	leading chan struct{}
	result  chan error
}

func startCandidate(client kubernetes.Interface, identity string, run func(context.Context) error) *candidate {
	ctx, cancel := context.WithCancel(context.Background())
	c := &candidate{
		gauge:   newGauge(),
		cancel:  cancel,
		leading: make(chan struct{}),
		result:  make(chan error, 1),
	}
	go func() {
		c.result <- Run(ctx, client, ns, identity, fastTimings, c.gauge, func(leadCtx context.Context) error {
			close(c.leading)
			if run != nil {
				return run(leadCtx)
			}
			<-leadCtx.Done()
			return nil
		})
	}()
	return c
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitResult(t *testing.T, c *candidate) error {
	t.Helper()
	select {
	case err := <-c.result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func leaseHolder(t *testing.T, client kubernetes.Interface) string {
	t.Helper()
	lease, err := client.CoordinationV1().Leases(ns).Get(context.Background(), LeaseName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting lease: %v", err)
	}
	if lease.Spec.HolderIdentity == nil {
		return ""
	}
	return *lease.Spec.HolderIdentity
}

func TestRunLeadsAndReleasesOnCancel(t *testing.T) {
	client := fake.NewSimpleClientset()
	a := startCandidate(client, "pod-a", nil)

	waitFor(t, a.leading, "pod-a to lead")
	if got := testutil.ToFloat64(a.gauge); got != 1 {
		t.Errorf("leader gauge while leading = %v, want 1", got)
	}
	if got := leaseHolder(t, client); got != "pod-a" {
		t.Errorf("lease holder = %q, want pod-a", got)
	}

	a.cancel()
	if err := waitResult(t, a); err != nil {
		t.Errorf("Run after cancel = %v, want nil", err)
	}
	if got := testutil.ToFloat64(a.gauge); got != 0 {
		t.Errorf("leader gauge after stopping = %v, want 0", got)
	}
	if got := leaseHolder(t, client); got != "" {
		t.Errorf("lease holder after release = %q, want empty", got)
	}
}

func TestRunReturnsRunError(t *testing.T) {
	client := fake.NewSimpleClientset()
	boom := errors.New("informer sync failed")
	a := startCandidate(client, "pod-a", func(context.Context) error { return boom })
	defer a.cancel()

	if err := waitResult(t, a); !errors.Is(err, boom) {
		t.Errorf("Run = %v, want %v", err, boom)
	}
	// The failed replica must give the Lease up so a healthy one can lead.
	if got := leaseHolder(t, client); got != "" {
		t.Errorf("lease holder after run failed = %q, want empty", got)
	}
}

func TestOnlyOneCandidateLeads(t *testing.T) {
	client := fake.NewSimpleClientset()
	a := startCandidate(client, "pod-a", nil)
	waitFor(t, a.leading, "pod-a to lead")

	b := startCandidate(client, "pod-b", nil)
	defer b.cancel()

	select {
	case <-b.leading:
		t.Fatal("pod-b started leading while pod-a held the lease")
	case <-time.After(3 * fastTimings.LeaseDuration):
	}

	// Once the leader steps down, the standby takes over.
	a.cancel()
	if err := waitResult(t, a); err != nil {
		t.Errorf("pod-a Run = %v, want nil", err)
	}
	waitFor(t, b.leading, "pod-b to take over")
	if got := leaseHolder(t, client); got != "pod-b" {
		t.Errorf("lease holder = %q, want pod-b", got)
	}
}
