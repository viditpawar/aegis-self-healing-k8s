package remediate

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCrashLoopDecision(t *testing.T) {
	cases := []struct {
		name         string
		restartCount int32
		waitReason   string
		wantDelete   bool
	}{
		{"6 restarts with CrashLoopBackOff reason", 6, "CrashLoopBackOff", true},
		{"2 restarts with CrashLoopBackOff reason", 2, "CrashLoopBackOff", false},
		{"exactly DefaultMaxRestarts is not enough", DefaultMaxRestarts, "CrashLoopBackOff", false},
		{"many restarts but waiting for another reason", 10, "ImagePullBackOff", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							RestartCount: tc.restartCount,
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{Reason: tc.waitReason},
							},
						},
					},
				},
			}

			got, _ := DefaultPolicy().CrashLoopDecision(pod)
			if got != tc.wantDelete {
				t.Errorf("CrashLoopDecision() = %v, want %v", got, tc.wantDelete)
			}
		})
	}
}

func TestPendingDecision(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name       string
		age        time.Duration
		wantDelete bool
	}{
		{"pending for 10 minutes", 10 * time.Minute, true},
		{"pending for 1 minute", 1 * time.Minute, false},
		{"pending for exactly DefaultPendingTimeout", DefaultPendingTimeout, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.NewTime(now.Add(-tc.age)),
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
				},
			}

			got, _ := DefaultPolicy().PendingDecision(pod, now)
			if got != tc.wantDelete {
				t.Errorf("PendingDecision() = %v, want %v", got, tc.wantDelete)
			}
		})
	}
}

func TestCrashLoopDecisionRunningContainer(t *testing.T) {
	pod := corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				RestartCount: 10,
				State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
	if got, _ := DefaultPolicy().CrashLoopDecision(pod); got {
		t.Error("CrashLoopDecision() = true for a running container, want false")
	}
}

func TestPendingDecisionIgnoresRunningPods(t *testing.T) {
	now := time.Now()
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if got, _ := DefaultPolicy().PendingDecision(pod, now); got {
		t.Error("PendingDecision() = true for a Running pod, want false")
	}
}

func TestPolicyThresholdsAreConfigurable(t *testing.T) {
	now := time.Now()
	strict := Policy{MaxRestarts: 1, PendingTimeout: 30 * time.Second}

	crashing := corev1.Pod{
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				RestartCount: 2,
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
			}},
		},
	}
	if got, _ := strict.CrashLoopDecision(crashing); !got {
		t.Error("strict policy should delete a pod with 2 restarts")
	}
	if got, _ := DefaultPolicy().CrashLoopDecision(crashing); got {
		t.Error("default policy should keep a pod with 2 restarts")
	}

	pending := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(-time.Minute))},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	if got, _ := strict.PendingDecision(pending, now); !got {
		t.Error("strict policy should delete a pod Pending for 1 minute")
	}
	if got, _ := DefaultPolicy().PendingDecision(pending, now); got {
		t.Error("default policy should keep a pod Pending for 1 minute")
	}
}

func TestOptOutAnnotation(t *testing.T) {
	now := time.Now()
	optedOut := metav1.ObjectMeta{
		Annotations:       map[string]string{OptOutAnnotation: "false"},
		CreationTimestamp: metav1.NewTime(now.Add(-time.Hour)),
	}

	crashing := corev1.Pod{
		ObjectMeta: optedOut,
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				RestartCount: 50,
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
			}},
		},
	}
	if got, _ := DefaultPolicy().CrashLoopDecision(crashing); got {
		t.Error("opted-out crash-looping pod should be kept")
	}

	pending := corev1.Pod{ObjectMeta: optedOut, Status: corev1.PodStatus{Phase: corev1.PodPending}}
	if got, _ := DefaultPolicy().PendingDecision(pending, now); got {
		t.Error("opted-out Pending pod should be kept")
	}

	// Any other value, including "true", leaves remediation on.
	crashing.Annotations = map[string]string{OptOutAnnotation: "true"}
	if got, _ := DefaultPolicy().CrashLoopDecision(crashing); !got {
		t.Error(`annotation "true" should not opt out`)
	}
}
