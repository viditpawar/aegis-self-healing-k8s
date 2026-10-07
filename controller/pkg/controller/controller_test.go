package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"

	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/config"
	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/remediate"
)

const ns = "aegis-workloads"

func newTestController(t *testing.T, pods ...*corev1.Pod) (*Controller, *fake.Clientset, *record.FakeRecorder) {
	t.Helper()
	client := fake.NewSimpleClientset()
	for _, p := range pods {
		if _, err := client.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding pod %s: %v", p.Name, err)
		}
	}
	recorder := record.NewFakeRecorder(10)
	cfg := config.Config{Namespace: ns, Policy: remediate.DefaultPolicy()}
	c := New(client, recorder, cfg, Counters{
		CrashLoop: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_crashloop"}),
		Pending:   prometheus.NewCounter(prometheus.CounterOpts{Name: "test_pending"}),
		Throttled: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_throttled"}),
	})
	return c, client, recorder
}

func crashLoopingPod(name string, restarts int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name + "-uid")},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				RestartCount: restarts,
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
			}},
		},
	}
}

func pendingPod(name string, created time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, UID: types.UID(name + "-uid"),
			CreationTimestamp: metav1.NewTime(created),
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
}

func podExists(t *testing.T, client *fake.Clientset, name string) bool {
	t.Helper()
	_, err := client.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatalf("getting pod %s: %v", name, err)
	}
	return true
}

func TestHandlePod(t *testing.T) {
	cases := []struct {
		name        string
		pod         *corev1.Pod
		wantDeleted bool
	}{
		{"crash-looping pod is deleted", crashLoopingPod("bad", 6), true},
		{"pod under restart threshold is kept", crashLoopingPod("flaky", 2), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, client, recorder := newTestController(t, tc.pod)

			c.HandlePod(context.Background(), tc.pod)

			if got := !podExists(t, client, tc.pod.Name); got != tc.wantDeleted {
				t.Errorf("deleted = %v, want %v", got, tc.wantDeleted)
			}
			wantCount := 0.0
			if tc.wantDeleted {
				wantCount = 1
			}
			if got := testutil.ToFloat64(c.counters.CrashLoop); got != wantCount {
				t.Errorf("crashloop counter = %v, want %v", got, wantCount)
			}
			if got := len(recorder.Events); got != int(wantCount) {
				t.Errorf("recorded %d events, want %v", got, wantCount)
			}
		})
	}
}

func TestHandlePodIgnoresNonPods(t *testing.T) {
	c, _, _ := newTestController(t)
	c.HandlePod(context.Background(), "not a pod")
	if got := testutil.ToFloat64(c.counters.CrashLoop); got != 0 {
		t.Errorf("crashloop counter = %v, want 0", got)
	}
}

func TestHandlePodSkipsTerminatingPod(t *testing.T) {
	pod := crashLoopingPod("terminating", 6)
	now := metav1.Now()
	pod.DeletionTimestamp = &now
	c, client, _ := newTestController(t, pod)

	c.HandlePod(context.Background(), pod)

	if !podExists(t, client, pod.Name) {
		t.Error("terminating pod was deleted again")
	}
}

// A resync racing a real update delivers the same pod twice; it must only
// be deleted and counted once.
func TestHandlePodDedupesSameUID(t *testing.T) {
	pod := crashLoopingPod("bad", 6)
	c, client, _ := newTestController(t, pod)

	c.HandlePod(context.Background(), pod)
	// Recreate so a second delete would succeed if dedup didn't stop it.
	if _, err := client.CoreV1().Pods(ns).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("recreating pod: %v", err)
	}
	c.HandlePod(context.Background(), pod)

	if got := testutil.ToFloat64(c.counters.CrashLoop); got != 1 {
		t.Errorf("crashloop counter = %v, want 1", got)
	}
	if !podExists(t, client, pod.Name) {
		t.Error("second event for the same UID deleted the pod again")
	}
}

func TestMarkInFlightExpires(t *testing.T) {
	c, _, _ := newTestController(t)
	now := time.Now()
	c.now = func() time.Time { return now }

	if !c.markInFlight("a") {
		t.Fatal("first mark should succeed")
	}
	if c.markInFlight("a") {
		t.Fatal("second mark within TTL should be rejected")
	}

	now = now.Add(inFlightTTL + time.Second)
	if !c.markInFlight("a") {
		t.Error("mark after TTL should succeed")
	}
	if len(c.inFlight) != 1 {
		t.Errorf("inFlight has %d entries, want 1 (expired entries pruned)", len(c.inFlight))
	}
}

func TestSweepPending(t *testing.T) {
	now := time.Now()
	stuck := pendingPod("stuck", now.Add(-10*time.Minute))
	fresh := pendingPod("fresh", now.Add(-1*time.Minute))
	c, client, recorder := newTestController(t, stuck, fresh)
	c.now = func() time.Time { return now }

	c.SweepPending(context.Background())

	if podExists(t, client, stuck.Name) {
		t.Error("stuck Pending pod was not deleted")
	}
	if !podExists(t, client, fresh.Name) {
		t.Error("fresh Pending pod was deleted")
	}
	if got := testutil.ToFloat64(c.counters.Pending); got != 1 {
		t.Errorf("pending counter = %v, want 1", got)
	}
	select {
	case ev := <-recorder.Events:
		if !strings.HasPrefix(ev, "Warning "+ReasonPendingRemediated+" ") {
			t.Errorf("event = %q, want a Warning %s event", ev, ReasonPendingRemediated)
		}
	default:
		t.Error("no event recorded for the deleted Pending pod")
	}
}

func TestCrashLoopEvent(t *testing.T) {
	pod := crashLoopingPod("bad", 6)
	c, _, recorder := newTestController(t, pod)

	c.HandlePod(context.Background(), pod)

	select {
	case ev := <-recorder.Events:
		want := "Warning " + ReasonCrashLoopRemediated + " Deleted by aegis-controller to force reschedule: CrashLoopBackOff, restart count 6"
		if ev != want {
			t.Errorf("event = %q, want %q", ev, want)
		}
	default:
		t.Error("no event recorded for the deleted crash-looping pod")
	}
}

func TestHandlePodUsesConfiguredPolicy(t *testing.T) {
	pod := crashLoopingPod("flaky", 2)
	c, client, _ := newTestController(t, pod)
	c.cfg.Policy.MaxRestarts = 1

	c.HandlePod(context.Background(), pod)

	if podExists(t, client, pod.Name) {
		t.Error("pod over the configured MaxRestarts of 1 was not deleted")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	c, _, _ := newTestController(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

func TestRateLimitThrottlesAndRetries(t *testing.T) {
	pods := []*corev1.Pod{crashLoopingPod("a", 6), crashLoopingPod("b", 6), crashLoopingPod("c", 6)}
	client := fake.NewSimpleClientset()
	for _, p := range pods {
		if _, err := client.CoreV1().Pods(ns).Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding pod %s: %v", p.Name, err)
		}
	}
	cfg := config.Config{Namespace: ns, Policy: remediate.DefaultPolicy(), MaxDeletionsPerMinute: 2}
	c := New(client, record.NewFakeRecorder(10), cfg, Counters{
		CrashLoop: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_crashloop"}),
		Pending:   prometheus.NewCounter(prometheus.CounterOpts{Name: "test_pending"}),
		Throttled: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_throttled"}),
	})
	now := time.Now()
	c.now = func() time.Time { return now }

	for _, p := range pods {
		c.HandlePod(context.Background(), p)
	}
	if got := testutil.ToFloat64(c.counters.CrashLoop); got != 2 {
		t.Errorf("deleted %v pods with a limit of 2/min, want 2", got)
	}
	if got := testutil.ToFloat64(c.counters.Throttled); got != 1 {
		t.Errorf("throttled = %v, want 1", got)
	}
	if !podExists(t, client, "c") {
		t.Fatal("third pod was deleted despite the rate limit")
	}

	// Half a minute later one token has refilled; the throttled pod's next
	// event goes through because its UID wasn't left marked in-flight.
	now = now.Add(30 * time.Second)
	c.HandlePod(context.Background(), pods[2])
	if podExists(t, client, "c") {
		t.Error("throttled pod was not deleted after the limit refilled")
	}
}

func TestOptedOutPodIsKept(t *testing.T) {
	pod := crashLoopingPod("debugging", 50)
	pod.Annotations = map[string]string{remediate.OptOutAnnotation: "false"}
	c, client, recorder := newTestController(t, pod)

	c.HandlePod(context.Background(), pod)

	if !podExists(t, client, pod.Name) {
		t.Error("opted-out pod was deleted")
	}
	if len(recorder.Events) != 0 {
		t.Errorf("recorded %d events for an opted-out pod, want 0", len(recorder.Events))
	}
}
