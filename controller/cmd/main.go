package main

import (
	"context"
	"fmt"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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

	log.Println("Aegis controller started, watching pods")

	for {
		pods, err := clientset.CoreV1().Pods("aegis-workloads").List(
			context.TODO(), metav1.ListOptions{})
		if err != nil {
			log.Printf("error listing pods: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}

		for _, pod := range pods.Items {
			remediateIfNeeded(clientset, pod)
		}

		time.Sleep(15 * time.Second)
	}
}

func remediateIfNeeded(clientset *kubernetes.Clientset, pod corev1.Pod) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount > 5 && cs.State.Waiting != nil &&
			cs.State.Waiting.Reason == "CrashLoopBackOff" {
			fmt.Printf("Pod %s in CrashLoopBackOff, restart count %d, deleting to force reschedule\n",
				pod.Name, cs.RestartCount)
			clientset.CoreV1().Pods(pod.Namespace).Delete(
				context.TODO(), pod.Name, metav1.DeleteOptions{})
		}
	}

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
