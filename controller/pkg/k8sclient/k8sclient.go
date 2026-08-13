// Package k8sclient builds the in-cluster Kubernetes clientset used by the
// controller.
package k8sclient

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// New builds a clientset using the in-cluster config supplied by the
// ServiceAccount token mounted into the controller's pod.
func New() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}
