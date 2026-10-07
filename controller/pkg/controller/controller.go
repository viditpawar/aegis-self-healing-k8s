// Package controller wires the pure remediate decisions to the Kubernetes
// API: it watches pods through an informer, sweeps for stuck-Pending pods,
// and deletes anything remediate says is unhealthy.
package controller

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/config"
)

const (
	informerResync   = 30 * time.Second
	pendingSweepTick = 30 * time.Second

	// inFlightTTL bounds how long a pod UID is remembered as "already being
	// deleted", to absorb the race between an informer resync/real-update
	// event and the delete actually landing, without leaking memory forever.
	inFlightTTL = 2 * time.Minute

	// Event reasons recorded on a pod when it's remediated, visible via
	// `kubectl get events` or `kubectl describe pod`.
	ReasonCrashLoopRemediated = "CrashLoopRemediated"
	ReasonPendingRemediated   = "PendingRemediated"
	ReasonThrottled           = "RemediationThrottled"
)

// Counters are the metrics the controller increments on a successful
// delete. They're injected so tests can use isolated counters.
type Counters struct {
	CrashLoop prometheus.Counter
	Pending   prometheus.Counter
	Throttled prometheus.Counter
}

// Controller remediates unhealthy pods in a single namespace.
type Controller struct {
	client   kubernetes.Interface
	recorder record.EventRecorder
	cfg      config.Config
	counters Counters
	now      func() time.Time

	// limiter caps deletions per minute; nil means unlimited.
	limiter *rate.Limiter

	// inFlight deduplicates delete attempts for the same pod UID that
	// arrive close together (e.g. an informer resync racing a real
	// update), so a single crash-looping pod doesn't get double-deleted
	// and double-counted. Values are expiry times.
	inFlightMu sync.Mutex
	inFlight   map[types.UID]time.Time
}

// New returns a Controller watching cfg.Namespace through client, and
// recording an Event on every pod it remediates.
func New(client kubernetes.Interface, recorder record.EventRecorder, cfg config.Config, counters Counters) *Controller {
	c := &Controller{
		client:   client,
		recorder: recorder,
		cfg:      cfg,
		counters: counters,
		now:      time.Now,
		inFlight: map[types.UID]time.Time{},
	}
	if n := cfg.MaxDeletionsPerMinute; n > 0 {
		// A full bucket of n, refilled evenly over the minute.
		c.limiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(n)), n)
	}
	return c
}

// Run starts the pod informer and the Pending sweep, and blocks until ctx
// is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	factory := informers.NewSharedInformerFactoryWithOptions(
		c.client, informerResync,
		informers.WithNamespace(c.cfg.Namespace),
	)
	podInformer := factory.Core().V1().Pods().Informer()

	if _, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.HandlePod(ctx, obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.HandlePod(ctx, newObj)
		},
	}); err != nil {
		return fmt.Errorf("adding pod event handler: %w", err)
	}

	factory.Start(ctx.Done())
	defer factory.Shutdown()
	if !cache.WaitForCacheSync(ctx.Done(), podInformer.HasSynced) {
		if ctx.Err() != nil {
			return nil // shut down before the first sync finished
		}
		return fmt.Errorf("failed to sync pod informer cache")
	}

	// Poll on a ticker because informers only fire on state changes, and a
	// pod sitting idle in Pending never produces one.
	ticker := time.NewTicker(pendingSweepTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.SweepPending(ctx)
		}
	}
}

// HandlePod deletes obj if it's a crash-looping pod.
func (c *Controller) HandlePod(ctx context.Context, obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	if shouldDelete, reason := c.cfg.Policy.CrashLoopDecision(*pod); shouldDelete {
		c.deletePod(ctx, *pod, reason, ReasonCrashLoopRemediated, c.counters.CrashLoop)
	}
}

// SweepPending deletes every pod in the namespace that's been stuck Pending
// past the configured Pending timeout.
func (c *Controller) SweepPending(ctx context.Context) {
	pods, err := c.client.CoreV1().Pods(c.cfg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("error listing pods: %v", err)
		return
	}
	now := c.now()
	for _, pod := range pods.Items {
		if shouldDelete, reason := c.cfg.Policy.PendingDecision(pod, now); shouldDelete {
			c.deletePod(ctx, pod, reason, ReasonPendingRemediated, c.counters.Pending)
		}
	}
}

func (c *Controller) deletePod(ctx context.Context, pod corev1.Pod, reason, eventReason string, counter prometheus.Counter) {
	if pod.DeletionTimestamp != nil || !c.markInFlight(pod.UID) {
		return
	}
	if c.limiter != nil && !c.limiter.AllowN(c.now(), 1) {
		// Forget the UID so the next resync or sweep can try again once
		// the limit has refilled.
		c.clearInFlight(pod.UID)
		c.counters.Throttled.Inc()
		log.Printf("Pod %s: %s, but the deletion rate limit was reached; will retry", pod.Name, reason)
		c.recorder.Eventf(&pod, corev1.EventTypeWarning, ReasonThrottled,
			"aegis-controller deletion rate limit reached, will retry: %s", reason)
		return
	}

	log.Printf("Pod %s: %s, deleting to force reschedule", pod.Name, reason)
	if err := c.client.CoreV1().Pods(pod.Namespace).Delete(
		ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
		log.Printf("failed to delete pod %s: %v", pod.Name, err)
		return
	}
	counter.Inc()
	c.recorder.Eventf(&pod, corev1.EventTypeWarning, eventReason,
		"Deleted by aegis-controller to force reschedule: %s", reason)
}

// markInFlight returns true if uid was not already being processed, and
// records it as in-flight for inFlightTTL. Returns false if a delete for
// this UID is already underway. Expired entries are pruned on each call.
func (c *Controller) markInFlight(uid types.UID) bool {
	c.inFlightMu.Lock()
	defer c.inFlightMu.Unlock()

	now := c.now()
	for id, expiry := range c.inFlight {
		if now.After(expiry) {
			delete(c.inFlight, id)
		}
	}

	if _, exists := c.inFlight[uid]; exists {
		return false
	}
	c.inFlight[uid] = now.Add(inFlightTTL)
	return true
}

func (c *Controller) clearInFlight(uid types.UID) {
	c.inFlightMu.Lock()
	defer c.inFlightMu.Unlock()
	delete(c.inFlight, uid)
}
