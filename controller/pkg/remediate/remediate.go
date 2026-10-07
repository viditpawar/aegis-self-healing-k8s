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
	DefaultMaxRestarts    = 5
	DefaultPendingTimeout = 5 * time.Minute
)

// Policy holds the thresholds remediation decisions are made against.
type Policy struct {
	// MaxRestarts is how many restarts a CrashLoopBackOff container may
	// reach before its pod is deleted; the pod is deleted once it exceeds it.
	MaxRestarts int32
	// PendingTimeout is how long a pod may stay Pending before it's deleted.
	PendingTimeout time.Duration
}

// DefaultPolicy returns the thresholds used when nothing is configured.
func DefaultPolicy() Policy {
	return Policy{MaxRestarts: DefaultMaxRestarts, PendingTimeout: DefaultPendingTimeout}
}

// CrashLoopDecision reports whether pod should be deleted because it is
// stuck in CrashLoopBackOff past p.MaxRestarts.
func (p Policy) CrashLoopDecision(pod corev1.Pod) (shouldDelete bool, reason string) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount > p.MaxRestarts && cs.State.Waiting != nil &&
			cs.State.Waiting.Reason == "CrashLoopBackOff" {
			return true, fmt.Sprintf("CrashLoopBackOff, restart count %d", cs.RestartCount)
		}
	}
	return false, ""
}

// PendingDecision reports whether pod should be deleted because it has been
// stuck Pending for longer than p.PendingTimeout, relative to now.
func (p Policy) PendingDecision(pod corev1.Pod, now time.Time) (shouldDelete bool, reason string) {
	if pod.Status.Phase != corev1.PodPending {
		return false, ""
	}
	age := now.Sub(pod.CreationTimestamp.Time)
	if age > p.PendingTimeout {
		return true, fmt.Sprintf("stuck Pending for %v", age.Round(time.Second))
	}
	return false, ""
}
