package gcrcleaner

import (
	"flag"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func (gcrcleaner *GCRCleaner) InitializeKubernetesClient() {

	if _, ok := os.LookupEnv("KUBERNETES_SERVICE_PORT"); !ok {

		var kubeconfig *string
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
		} else {
			kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
		}
		flag.Parse()

		// use the current context in kubeconfig
		config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			panic(err.Error())
		}

		// create the clientset
		clientset, err := kubernetes.NewForConfig(config)
		gcrcleaner.KubernetesClient = clientset

	} else {

		config, err := rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
		// creates the clientset
		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			panic(err.Error())
		}

		gcrcleaner.KubernetesClient = clientset
	}
}
