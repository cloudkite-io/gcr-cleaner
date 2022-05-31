package gcrcleaner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	// "time"

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

func (gcrcleaner *GCRCleaner) shouldDelete(digest string, m google.ManifestInfo, repo string, since time.Time, tagFilter *regexp.Regexp, imageFilter *regexp.Regexp, clusterImages []string) bool {
	// Immediately exclude images that have been uploaded after the given time.
	if uploaded := m.Uploaded.UTC(); uploaded.After(since) {
		fmt.Println("should not delete",
			"repo", repo,
			"digest", digest,
			"reason", "too new",
			"since", since.Format(time.RFC3339),
			"uploaded", uploaded.Format(time.RFC3339),
			"delta", uploaded.Sub(since).String())
		return false
	}

	// If tagged images are allowed and the given filter matches the list of tags,
	// this is a deletion candidate. The default tag filter is to reject all
	// strings.
	for _, tag := range m.Tags {
		imagename := gcrcleaner.Config.CleanerConf.REGISTRY + "/" + repo + ":" + tag

		if tagFilter.MatchString(tag) {
			fmt.Println("Regex " + tagFilter.String() + " matches " + tag)
			fmt.Println("should not delete",
				"imagename", imagename,
				"digest", digest,
				"reason", "matches tag filter",
				"tag_filter", tagFilter)
			return false

		}

		if imageFilter.MatchString(imagename) {
			fmt.Println("should not delete",
				"imagename", imagename,
				"digest", digest,
				"reason", "matches images filter",
				"tag", tag)
			return false

		}

		if stringInSlice(imagename, clusterImages) {
			fmt.Println("should not delete",
				"imagename", imagename,
				"digest", digest,
				"reason", "image in use in clusters",
				"tag", tag)
			return false
		}

	}

	// repo refers to

	// If we got this far, it'ts not a viable deletion candidate.
	fmt.Println("should delete",
		"repo", repo,
		"digest", digest,
		"reason", "no filter matches")
	return true

}

func (gcrcleaner *GCRCleaner) deleteOne(ctx context.Context, ref gcr.Reference) error {
	if err := remote.Delete(ref,
		remote.WithAuth(gcrcleaner.Auth),
		remote.WithContext(ctx)); err != nil {
		return fmt.Errorf("failed to delete %s: %w", ref, err)
	}

	return nil
}

type manifestStruct struct {
	Repo   string
	Digest string
	Info   gcrgoogle.ManifestInfo
}

func (gcrcleaner *GCRCleaner) Delete(ctx context.Context, imagesToDelete manifestStruct) {
	// Create a worker pool for parallel deletion
	pool := workerpool.New(c.concurrency)

	var deleted = make([]string, 0, len(imagesToDelete))
	var deletedLock sync.Mutex
	var errs = make(map[string]error)
	var errsLock sync.RWMutex
	for _, imageToDelete := range imagesToDelete {
		for _, tag := range imageToDelete.Info.Tags {
			c.logger.Debug("deleting tag",
				"repo", imageToDelete.Repo,
				"digest", imageToDelete.Digest,
				"tag", tag)

			tagged := gcrrepo.Tag(tag)
			if !dryRun {
				if err := gcrcleaner.deleteOne(ctx, tagged); err != nil {
					return nil, fmt.Errorf("failed to delete %s: %w", tagged, err)
				}
			}

			deletedLock.Lock()
			deleted = append(deleted, tagged.Identifier())
			deletedLock.Unlock()
		}

		digest := imageToDelete.Digest
		ref := gcrrepo.Digest(digest)
		pool.Submit(func() {
			// Do not process if previous invocations failed. This prevents a large
			// build-up of failed requests and rate limit exceeding (e.g. bad auth).
			errsLock.RLock()
			if len(errs) > 0 {
				errsLock.RUnlock()
				return
			}
			errsLock.RUnlock()

			c.logger.Debug("deleting digest",
				"repo", repo,
				"digest", imageToDelete.Digest)

			if !dryRun {
				if err := c.deleteOne(ctx, ref); err != nil {
					cause := errors.Unwrap(err).Error()

					errsLock.Lock()
					if _, ok := errs[cause]; !ok {
						errs[cause] = err
						errsLock.Unlock()
						return
					}
					errsLock.Unlock()
				}
			}

			deletedLock.Lock()
			deleted = append(deleted, digest)
			deletedLock.Unlock()
		})
	}

	// Wait for everything to finish
	pool.StopWait()

	// Aggregate any errors
	if len(errs) > 0 {
		var errStrings []string
		for _, v := range errs {
			errStrings = append(errStrings, v.Error())
		}

		if len(errStrings) == 1 {
			return nil, fmt.Errorf(errStrings[0])
		}

		return nil, fmt.Errorf("%d errors occurred: %s",
			len(errStrings), strings.Join(errStrings, ", "))
	}

	sort.Strings(deleted)

	return deleted, nil
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

	imageFilter, err := regexp.Compile(gcrcleaner.Config.CleanerConf.OMIT_IMAGES_REGEX)
	if err != nil {
		log.Fatalf("ERROR: %s", err)
	}

	tagFilter, err := regexp.Compile(gcrcleaner.Config.CleanerConf.OMIT_TAGS_REGEX)
	if err != nil {
		log.Fatalf("ERROR: %s", err)
	}

	ageDays, err := strconv.Atoi(gcrcleaner.Config.CleanerConf.AGE_DAYS)
	if err != nil {
		log.Fatalf("ERROR: %s", err)
	}
	since := time.Now().AddDate(0, 0, -ageDays)

	clusterImages := gcrcleaner.GetKubernetesImages(ctx)

	// imagesToDelete := []string{}
	imagesToDelete := []*manifestStruct{}

	for _, repo := range repos {
		if strings.Contains(repo, gcrcleaner.Config.CleanerConf.PROJECT_ID) {
			gcrrepo, err := gcr.NewRepository(gcrcleaner.Config.CleanerConf.REGISTRY + "/" + repo)
			if err != nil {
				log.Fatalf("ERROR: %s", err)
			}

			imageinfo, err := google.List(gcrrepo,
				google.WithContext(ctx),
				google.WithAuth(gcrcleaner.Auth))
			if err != nil {
				log.Fatalf("ERROR: %s", err)
			}
			for digest, manifest := range imageinfo.Manifests {

				to_delete := gcrcleaner.shouldDelete(digest, manifest, repo, since, tagFilter, imageFilter, clusterImages)
				if to_delete {
					imagesToDelete = append(imagesToDelete, &manifestStruct(repo, digest, manifest))
				}

			}

		}
	}
}
