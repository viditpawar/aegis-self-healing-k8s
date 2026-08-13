// Package remediate holds the pure decision logic for whether a pod should
// be remediated. Functions here only inspect pod state and return a
// decision; they never talk to the Kubernetes API themselves.
package remediate

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	MaxRestarts    = 5
	PendingTimeout = 5 * time.Minute
)

// CrashLoopDecision reports whether pod should be deleted because it is
// stuck in CrashLoopBackOff past MaxRestarts.
func CrashLoopDecision(pod corev1.Pod) (shouldDelete bool, reason string) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount > MaxRestarts && cs.State.Waiting != nil &&
			cs.State.Waiting.Reason == "CrashLoopBackOff" {
			return true, fmt.Sprintf("CrashLoopBackOff, restart count %d", cs.RestartCount)
		}
	}
	return false, ""
}

// PendingDecision reports whether pod should be deleted because it has been
// stuck Pending for longer than PendingTimeout, relative to now.
func PendingDecision(pod corev1.Pod, now time.Time) (shouldDelete bool, reason string) {
	if pod.Status.Phase != corev1.PodPending {
		return false, ""
	}
	age := now.Sub(pod.CreationTimestamp.Time)
	if age > PendingTimeout {
		return true, fmt.Sprintf("stuck Pending for %v", age)
	}
	return false, ""
}
