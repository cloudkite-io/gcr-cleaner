package gcrcleaner

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/cloudkite-io/gcr-cleaner/pkg/config"
	gcrauthn "github.com/google/go-containerregistry/pkg/authn"
	gcr "github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type GCRCleaner struct {
	Config     *config.Conf
	Auth       gcrauthn.Authenticator
	KubeConfig string
}

// ensures required env variables are set and adds auth
func (gcrcleaner *GCRCleaner) InitializeConfig() {
	gcrcleaner.Config = config.AppConfig()
	auth, err := google.NewEnvAuthenticator()
	if err != nil {
		log.Fatalf("ERROR: %s", err)
	}

	gcrcleaner.Auth = auth
}

func (gcrcleaner *GCRCleaner) GetKubernetesImages(ctx context.Context) []string {
	kubecontexts := strings.Split(gcrcleaner.Config.CleanerConf.KUBERNETES_CONTEXTS, ",")
	var containers []string
	for _, kubecontext := range kubecontexts {

		config, err := buildConfigFromFlags(kubecontext, gcrcleaner.KubeConfig)
		if err != nil {
			log.Fatalf("ERROR: %s", err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			log.Fatalf("ERROR: %s", err)
		}

		pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		if err != nil {
			log.Fatalf("ERROR: %s", err)
		}

		for _, pod := range pods.Items {
			if pod.Status.Phase == "Running" {
				for _, container := range pod.Spec.Containers {
					containers = append(containers, container.Image)
				}
			}
		}
	}

	return containers
}

func (gcrcleaner *GCRCleaner) Clean(ctx context.Context) {
	registry, err := gcr.NewRegistry(gcrcleaner.Config.CleanerConf.REGISTRY)
	if err != nil {
		log.Fatalf("ERROR: %s", err)
	}

	repos, err := remote.Catalog(ctx, registry, remote.WithAuth(gcrcleaner.Auth))
	if err != nil {
		log.Fatalf("ERROR: %s", err)
	}
	containerimages := gcrcleaner.GetKubernetesImages(ctx)

	for _, repo := range repos {
		if strings.Contains(repo, gcrcleaner.Config.CleanerConf.PROJECT_ID) {
			gcrrepo, err := gcr.NewRepository(gcrcleaner.Config.CleanerConf.REGISTRY + "/" + repo)
			if err != nil {
				log.Fatalf("ERROR: %s", err)
			}

			tags, err := google.List(gcrrepo,
				google.WithContext(ctx),
				google.WithAuth(gcrcleaner.Auth))
			if err != nil {
				log.Fatalf("ERRORx: %s", err)
			}
			for _, manifest := range tags.Manifests {
				for _, tag := range manifest.Tags {
					if stringInSlice(gcrcleaner.Config.CleanerConf.REGISTRY+"/"+repo+":"+tag, containerimages) {
						fmt.Println("Dont delete:" + gcrcleaner.Config.CleanerConf.REGISTRY + "/" + repo + ":" + tag)
					} else {
						fmt.Println("Delete: " + gcrcleaner.Config.CleanerConf.REGISTRY + "/" + repo + ":" + tag)

					}
				}
			}

		}
	}
}
