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

			got, _ := CrashLoopDecision(pod)
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

			got, _ := PendingDecision(pod, now)
			if got != tc.wantDelete {
				t.Errorf("PendingDecision() = %v, want %v", got, tc.wantDelete)
			}
		})
	}
}
