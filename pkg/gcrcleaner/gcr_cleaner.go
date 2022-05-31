package gcrcleaner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	// "time"

	"github.com/cloudkite-io/gcr-cleaner/pkg/config"
	"github.com/gammazero/workerpool"
	gcrauthn "github.com/google/go-containerregistry/pkg/authn"
	gcr "github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type GCRCleaner struct {
	Config      *config.Conf
	Auth        gcrauthn.Authenticator
	KubeConfig  string
	concurrency int
	log         *logrus.Logger
}

type manifestStruct struct {
	Repo   string
	Digest string
	Info   google.ManifestInfo
}

// ensures required env variables are set and adds auth
func (gcrcleaner *GCRCleaner) InitializeConfig() {

	gcrcleaner.log = logrus.New()
	gcrcleaner.log.SetLevel(logrus.InfoLevel)
	gcrcleaner.log.SetOutput(os.Stdout)

	gcrcleaner.Config = config.AppConfig()
	auth, err := google.NewEnvAuthenticator()

	if err != nil {
		gcrcleaner.log.Fatalf("ERROR: %s", err)
	}
	gcrcleaner.concurrency = 3
	gcrcleaner.Auth = auth

}

func (gcrcleaner *GCRCleaner) GetKubernetesImages(ctx context.Context) []string {
	kubecontexts := strings.Split(gcrcleaner.Config.CleanerConf.KUBERNETES_CONTEXTS, ",")
	var containers []string
	for _, kubecontext := range kubecontexts {

		config, err := buildConfigFromFlags(kubecontext, gcrcleaner.KubeConfig)
		if err != nil {
			gcrcleaner.log.Fatalf("ERROR: %s", err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			gcrcleaner.log.Fatalf("ERROR: %s", err)
		}

		pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		if err != nil {
			gcrcleaner.log.Fatalf("ERROR: %s", err)
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

func (gcrcleaner *GCRCleaner) shouldDelete(m *manifestStruct, since time.Time, tagFilter *regexp.Regexp, imageFilter *regexp.Regexp, clusterImages []string) bool {
	// Immediately exclude images that have been uploaded after the given time.
	if uploaded := m.Info.Uploaded.UTC(); uploaded.After(since) {
		gcrcleaner.log.Debug("should not delete",
			"repo", m.Repo,
			"digest", m.Digest,
			"reason", "too new",
			"since", since.Format(time.RFC3339),
			"uploaded", uploaded.Format(time.RFC3339),
			"delta", uploaded.Sub(since).String())
		return false
	}

	// If tagged images are allowed and the given filter matches the list of tags,
	// this is a deletion candidate. The default tag filter is to reject all
	// strings.
	for _, tag := range m.Info.Tags {
		image_name_with_tag := gcrcleaner.Config.CleanerConf.REGISTRY + "/" + m.Repo + ":" + tag

		if tagFilter.MatchString(tag) {
			gcrcleaner.log.Debug("should not delete ",
				"imagename", image_name_with_tag,
				"digest", m.Digest,
				"reason", "matches tag filter",
				"tag_filter", tagFilter)
			return false

		}

		if imageFilter.MatchString(image_name_with_tag) {
			gcrcleaner.log.Debug("should not delete ",
				"imagename", image_name_with_tag,
				"digest", m.Digest,
				"reason", "matches images filter",
				"tag", tag)
			return false

		}

		if stringInSlice(image_name_with_tag, clusterImages) {
			gcrcleaner.log.Debug("should not delete ",
				"imagename", image_name_with_tag,
				"digest", m.Digest,
				"reason", "image in use in clusters",
				"tag", tag)
			return false
		}

	}

	// cater for images filter that do not have tags
	image_name_with_digest := gcrcleaner.Config.CleanerConf.REGISTRY + "/" + m.Repo + "@" + m.Digest
	if imageFilter.MatchString(image_name_with_digest) {
		gcrcleaner.log.Debug("should not delete ",
			"imagename", image_name_with_digest,
			"digest", m.Digest,
			"reason", "matches images filter ",
		)
		return false

	}
	// repo refers to

	// If we got this far, it'ts not a viable deletion candidate.
	gcrcleaner.log.Debug("should delete ",
		"repo", m.Repo,
		"digest", m.Digest,
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

func (gcrcleaner *GCRCleaner) Delete(ctx context.Context, imagesToDelete []*manifestStruct) {
	// Create a worker pool for parallel deletion
	pool := workerpool.New(gcrcleaner.concurrency)

	var deleted = make([]string, 0, len(imagesToDelete))
	var deletedLock sync.Mutex
	var errs = make(map[string]error)
	var errsLock sync.RWMutex
	for _, imageToDelete := range imagesToDelete {
		gcrrepo, err := gcr.NewRepository(gcrcleaner.Config.CleanerConf.REGISTRY + "/" + imageToDelete.Repo)
		if err != nil {
			gcrcleaner.log.Fatalf("ERROR: %s", err)
		}
		for _, tag := range imageToDelete.Info.Tags {
			gcrcleaner.log.Debug("deleting tag",
				"repo", imageToDelete.Repo,
				"digest", imageToDelete.Digest,
				"tag", tag)

			tagged := gcrrepo.Tag(tag)

			if err := gcrcleaner.deleteOne(ctx, tagged); err != nil {
				gcrcleaner.log.Fatalf("failed to delete %s: %w", tagged, err)
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

			gcrcleaner.log.Debug("deleting digest",
				"repo", imageToDelete.Repo,
				"digest", imageToDelete.Digest)

			if err := gcrcleaner.deleteOne(ctx, ref); err != nil {
				cause := errors.Unwrap(err).Error()

				errsLock.Lock()
				if _, ok := errs[cause]; !ok {
					errs[cause] = err
					errsLock.Unlock()
					return
				}
				errsLock.Unlock()
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
			gcrcleaner.log.Fatalf(errStrings[0])
		}

		gcrcleaner.log.Fatalf("%d errors occurred: %s", len(errStrings), strings.Join(errStrings, ", "))
	}

	sort.Strings(deleted)

}

func (gcrcleaner *GCRCleaner) Clean(ctx context.Context) {
	registry, err := gcr.NewRegistry(gcrcleaner.Config.CleanerConf.REGISTRY)
	if err != nil {
		gcrcleaner.log.Fatalf("ERROR: %s", err)
	}

	repos, err := remote.Catalog(ctx, registry, remote.WithAuth(gcrcleaner.Auth))
	if err != nil {
		gcrcleaner.log.Fatalf("ERROR: %s", err)
	}

	imageFilter, err := regexp.Compile(gcrcleaner.Config.CleanerConf.OMIT_IMAGES_REGEX)
	if err != nil {
		gcrcleaner.log.Fatalf("ERROR: %s", err)
	}

	tagFilter, err := regexp.Compile(gcrcleaner.Config.CleanerConf.OMIT_TAGS_REGEX)
	if err != nil {
		gcrcleaner.log.Fatalf("ERROR: %s", err)
	}

	ageDays, err := strconv.Atoi(gcrcleaner.Config.CleanerConf.AGE_DAYS)
	if err != nil {
		gcrcleaner.log.Fatalf("ERROR: %s", err)
	}
	since := time.Now().AddDate(0, 0, -ageDays)

	clusterImages := gcrcleaner.GetKubernetesImages(ctx)

	// imagesToDelete := []string{}
	var imagesToDelete []*manifestStruct

	gcrcleaner.log.Info("Checking for deletion candidates...")
	for _, repo := range repos {
		var repoManifests []*manifestStruct
		if strings.Contains(repo, gcrcleaner.Config.CleanerConf.PROJECT_ID) {
			gcrrepo, err := gcr.NewRepository(gcrcleaner.Config.CleanerConf.REGISTRY + "/" + repo)
			if err != nil {
				gcrcleaner.log.Fatalf("ERROR: %s", err)
			}

			imageinfo, err := google.List(gcrrepo,
				google.WithContext(ctx),
				google.WithAuth(gcrcleaner.Auth))
			if err != nil {
				gcrcleaner.log.Fatalf("ERROR: %s", err)
			}
			for digest, manifest := range imageinfo.Manifests {
				repoManifests = append(repoManifests, &manifestStruct{repo, digest, manifest})
			}

			sort.Slice(repoManifests, func(i, j int) bool {
				return repoManifests[j].Info.Created.Before(repoManifests[i].Info.Created)
			})

			if len(repoManifests) > 2 {

				repoManifests = repoManifests[2:]
				for _, manifest := range repoManifests {
					to_delete := gcrcleaner.shouldDelete(manifest, since, tagFilter, imageFilter, clusterImages)
					if to_delete {
						gcrcleaner.log.Info(gcrcleaner.Config.CleanerConf.REGISTRY + "/" + repo + "@" + manifest.Digest + " with tags " + strings.Join(manifest.Info.Tags, ","))
						imagesToDelete = append(imagesToDelete, &manifestStruct{repo, manifest.Digest, manifest.Info})
					}
				}
			}

		}

	}

	if len(imagesToDelete) == 0 {
		gcrcleaner.log.Info("There are no images to be deleted")
		return
	}
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Press y to delete  " + strconv.Itoa(len(imagesToDelete)) + " images listed above :")
	text, _ := reader.ReadString('\n')

	if strings.ToLower(text) == "y\n" {
		gcrcleaner.Delete(ctx, imagesToDelete)
		gcrcleaner.log.Info("Successful deletion of " + strconv.Itoa(len(imagesToDelete)) + " images")
	}

}
