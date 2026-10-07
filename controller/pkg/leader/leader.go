// Package leader runs a function only while this replica holds a Lease,
// so several controller replicas can run for availability without more
// than one of them remediating pods at a time.
package leader

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// LeaseName is the coordination.k8s.io Lease replicas compete for.
const LeaseName = "aegis-controller"

// ErrLostLeadership is returned when this replica stops being leader while
// its context is still live. Callers should exit rather than retry, so the
// pod restarts cleanly and no stale informer keeps acting as leader.
var ErrLostLeadership = errors.New("lost leadership")

// Timings controls how quickly leadership is acquired and handed over.
type Timings struct {
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
}

// DefaultTimings are client-go's usual values: a dead leader is replaced
// within about 15s.
var DefaultTimings = Timings{
	LeaseDuration: 15 * time.Second,
	RenewDeadline: 10 * time.Second,
	RetryPeriod:   2 * time.Second,
}

// Run blocks, competing for the Lease in namespace as identity. While this
// replica leads, isLeader is 1 and run is called with a context that's
// cancelled when leadership ends. It returns nil when ctx is cancelled,
// run's error if run fails, or ErrLostLeadership.
func Run(ctx context.Context, client kubernetes.Interface, namespace, identity string,
	timings Timings, isLeader prometheus.Gauge, run func(context.Context) error) error {

	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: LeaseName, Namespace: namespace},
		Client:     client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: identity},
	}

	// run's failure has to end the election too, so it gets its own
	// cancel; otherwise a crashed controller would keep holding the Lease.
	electionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// client-go fires OnStartedLeading in a goroutine it never waits on,
	// so the callback only hands over the leadership context and run
	// executes here, where its result can be collected safely.
	leading := make(chan context.Context, 1)

	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   timings.LeaseDuration,
		RenewDeadline:   timings.RenewDeadline,
		RetryPeriod:     timings.RetryPeriod,
		ReleaseOnCancel: true,
		Name:            LeaseName,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leadCtx context.Context) {
				leading <- leadCtx
			},
			OnStoppedLeading: func() {
				log.Printf("%s stopped leading", identity)
			},
			OnNewLeader: func(current string) {
				if current != identity {
					log.Printf("current leader is %s, standing by", current)
				}
			},
		},
	})
	if err != nil {
		return fmt.Errorf("configuring leader election: %w", err)
	}

	electionDone := make(chan struct{})
	go func() {
		elector.Run(electionCtx)
		close(electionDone)
	}()

	select {
	case <-electionDone:
		// Stopped before ever leading.
	case leadCtx := <-leading:
		// run returns once leadCtx is cancelled (ctx done or leadership
		// lost), or early if the controller itself fails.
		log.Printf("%s acquired leadership", identity)
		isLeader.Set(1)
		err := run(leadCtx)
		isLeader.Set(0)
		if err != nil {
			cancel()
			<-electionDone
			return err
		}
		<-electionDone
	}

	if ctx.Err() != nil {
		return nil
	}
	return ErrLostLeadership
}
