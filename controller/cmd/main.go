package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/k8sclient"
	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/metrics"
	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/remediate"
)

const (
	targetNamespace  = "aegis-workloads"
	informerResync   = 30 * time.Second
	pendingSweepTick = 30 * time.Second
	metricsAddr      = ":8080"

	// inFlightTTL bounds how long a pod UID is remembered as "already being
	// deleted", to absorb the race between an informer resync/real-update
	// event and the delete actually landing, without leaking memory forever.
	inFlightTTL = 2 * time.Minute
)

// inFlight deduplicates delete attempts for the same pod UID that arrive
// close together (e.g. an informer resync racing a real update), so a
// single crash-looping pod doesn't get double-deleted and double-counted.
var (
	inFlightMu sync.Mutex
	inFlight   = map[types.UID]struct{}{}
)

func main() {
	clientset, err := k8sclient.New()
	if err != nil {
		log.Fatalf("failed to create clientset: %v", err)
	}

	go metrics.Serve(metricsAddr)

	log.Println("Aegis controller started, watching pods in", targetNamespace)

	factory := informers.NewSharedInformerFactoryWithOptions(
		clientset, informerResync,
		informers.WithNamespace(targetNamespace),
	)
	podInformer := factory.Core().V1().Pods().Informer()

	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			onPodEvent(clientset, obj)
		},
		UpdateFunc: func(_, newObj interface{}) {
			onPodEvent(clientset, newObj)
		},
	})

	stopCh := make(chan struct{})
	defer close(stopCh)

	factory.Start(stopCh)
	if !cache.WaitForCacheSync(stopCh, podInformer.HasSynced) {
		log.Fatal("failed to sync pod informer cache")
	}

	go sweepPendingPods(clientset, pendingSweepTick)

	<-stopCh
}

func onPodEvent(clientset *kubernetes.Clientset, obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	if shouldDelete, reason := remediate.CrashLoopDecision(*pod); shouldDelete {
		deletePod(clientset, *pod, reason, metrics.CrashLoopDeletions)
	}
}

// sweepPendingPods polls on a ticker because informers only fire on state
// changes, and a pod sitting idle in Pending never produces one.
func sweepPendingPods(clientset *kubernetes.Clientset, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		pods, err := clientset.CoreV1().Pods(targetNamespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			log.Printf("error listing pods: %v", err)
			continue
		}
		for _, pod := range pods.Items {
			if shouldDelete, reason := remediate.PendingDecision(pod, time.Now()); shouldDelete {
				deletePod(clientset, pod, reason, metrics.PendingDeletions)
			}
		}
	}
}

func deletePod(clientset *kubernetes.Clientset, pod corev1.Pod, reason string, counter prometheus.Counter) {
	if pod.DeletionTimestamp != nil || !markInFlight(pod.UID) {
		return
	}

	log.Printf("Pod %s: %s, deleting to force reschedule", pod.Name, reason)
	if err := clientset.CoreV1().Pods(pod.Namespace).Delete(
		context.TODO(), pod.Name, metav1.DeleteOptions{}); err != nil {
		log.Printf("failed to delete pod %s: %v", pod.Name, err)
		return
	}
	counter.Inc()
}

// markInFlight returns true if uid was not already being processed, and
// records it as in-flight for inFlightTTL. Returns false if a delete for
// this UID is already underway.
func markInFlight(uid types.UID) bool {
	inFlightMu.Lock()
	defer inFlightMu.Unlock()

	if _, exists := inFlight[uid]; exists {
		return false
	}
	inFlight[uid] = struct{}{}

	go func() {
		time.Sleep(inFlightTTL)
		inFlightMu.Lock()
		delete(inFlight, uid)
		inFlightMu.Unlock()
	}()

	return true
}
