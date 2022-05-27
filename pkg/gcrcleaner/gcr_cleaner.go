package gcrcleaner

import (
	"github.com/cloudkite-io/gcr-cleaner/pkg/config"
	"k8s.io/client-go/kubernetes"
)

type GCRCleaner struct {
	KubernetesClient *kubernetes.Clientset
	Config           *config.Conf
}

// ensures required env variables are set
func (gcrcleaner *GCRCleaner) InitializeConfig() {
	gcrcleaner.Config = config.AppConfig()
}
