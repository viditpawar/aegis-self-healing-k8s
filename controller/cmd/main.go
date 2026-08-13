package main

import (
	"context"
	"fmt"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const (
	targetNamespace  = "aegis-workloads"
	informerResync   = 30 * time.Second
	pendingSweepTick = 30 * time.Second
)

func main() {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("failed to load in cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create clientset: %v", err)
	}

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
	remediateCrashLoop(clientset, *pod)
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
			remediatePending(clientset, pod)
		}
	}
}

func remediateCrashLoop(clientset *kubernetes.Clientset, pod corev1.Pod) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount > 5 && cs.State.Waiting != nil &&
			cs.State.Waiting.Reason == "CrashLoopBackOff" {
			fmt.Printf("Pod %s in CrashLoopBackOff, restart count %d, deleting to force reschedule\n",
				pod.Name, cs.RestartCount)
			clientset.CoreV1().Pods(pod.Namespace).Delete(
				context.TODO(), pod.Name, metav1.DeleteOptions{})
		}
	}
}

func remediatePending(clientset *kubernetes.Clientset, pod corev1.Pod) {
	if pod.Status.Phase == corev1.PodPending {
		age := time.Since(pod.CreationTimestamp.Time)
		if age > 5*time.Minute {
			fmt.Printf("Pod %s stuck Pending for %v, deleting to force reschedule\n",
				pod.Name, age)
			clientset.CoreV1().Pods(pod.Namespace).Delete(
				context.TODO(), pod.Name, metav1.DeleteOptions{})
		}
	}
}
