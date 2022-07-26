package gcr

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	app "github.com/cloudkite-io/gcr-cleaner"
	"github.com/cloudkite-io/gcr-cleaner/pkg/kube"
	"github.com/cloudkite-io/gcr-cleaner/pkg/utils"
	"github.com/gammazero/workerpool"
	gcrauthn "github.com/google/go-containerregistry/pkg/authn"
	cregistry "github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type service struct {
	Registry    app.Registry
	Auth        gcrauthn.Authenticator
	KubeConfig  string
	concurrency int
	log         *logrus.Logger
}

var _ app.CleanerService = &service{}

type manifestStruct struct {
	Repo   string
	Digest string
	Info   google.ManifestInfo
}

func NewService(registry app.Registry) app.CleanerService {
	gc := &service{
		Registry: registry,
		log:      logrus.New(),
	}
	gc.log.SetLevel(logrus.InfoLevel)
	gc.log.SetOutput(os.Stdout)
	gc.setRegexes()

	gc.KubeConfig = kube.GetKubeConfig()

	auth, err := google.NewEnvAuthenticator()
	if err != nil {
		gc.log.Fatalf("ERROR: %s", err)
	}
	gc.concurrency = 3
	gc.Auth = auth

	return gc
}

func (gcr *service) setRegexes() {
	// if regexes are not passed ensure that an impossible match is set
	if gcr.Registry.OmitImagesRegex == "" {
		gcr.Registry.OmitImagesRegex = "a^"
	}

	if gcr.Registry.OmitTagsRegex == "" {
		gcr.Registry.OmitTagsRegex = "a^"
	}
}

func (gcr *service) GetKubernetesImages(ctx context.Context) []string {
	var containers []string
	for _, kubecontext := range gcr.Registry.KubernetesContexts {

		config, err := kube.BuildConfigFromFlags(kubecontext, gcr.KubeConfig)
		if err != nil {
			gcr.log.Fatalf("ERROR: %s", err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			gcr.log.Fatalf("ERROR: %s", err)
		}

		pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		if err != nil {
			gcr.log.Fatalf("ERROR: %s", err)
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

func (gcr *service) shouldDelete(m *manifestStruct, since time.Time, tagFilter *regexp.Regexp, imageFilter *regexp.Regexp, clusterImages []string) bool {
	// Immediately exclude images that have been uploaded after the given time.
	if uploaded := m.Info.Uploaded.UTC(); uploaded.After(since) {
		gcr.log.Debug("should not delete",
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
		image_name_with_tag := gcr.Registry.RegistryHost + "/" + m.Repo + ":" + tag

		if tagFilter.MatchString(tag) {
			gcr.log.Debug("should not delete ",
				"imagename", image_name_with_tag,
				"digest", m.Digest,
				"reason", "matches tag filter",
				"tag_filter", tagFilter)
			return false

		}

		if imageFilter.MatchString(image_name_with_tag) {
			gcr.log.Debug("should not delete ",
				"imagename", image_name_with_tag,
				"digest", m.Digest,
				"reason", "matches images filter",
				"tag", tag)
			return false

		}

		if utils.StringInSlice(image_name_with_tag, clusterImages) {
			gcr.log.Debug("should not delete ",
				"imagename", image_name_with_tag,
				"digest", m.Digest,
				"reason", "image in use in clusters",
				"tag", tag)
			return false
		}

	}

	// cater for images filter that do not have tags
	image_name_with_digest := gcr.Registry.RegistryHost + "/" + m.Repo + "@" + m.Digest
	if imageFilter.MatchString(image_name_with_digest) {
		gcr.log.Debug("should not delete ",
			"imagename", image_name_with_digest,
			"digest", m.Digest,
			"reason", "matches images filter ",
		)
		return false

	}
	// repo refers to

	// If we got this far, it'ts not a viable deletion candidate.
	gcr.log.Debug("should delete ",
		"repo", m.Repo,
		"digest", m.Digest,
		"reason", "no filter matches")
	return true

}

func (gcr *service) deleteOne(ctx context.Context, ref cregistry.Reference) error {
	if err := remote.Delete(ref,
		remote.WithAuth(gcr.Auth),
		remote.WithContext(ctx)); err != nil {
		return fmt.Errorf("failed to delete %s: %w", ref, err)
	}

	return nil
}

func (gcr *service) Delete(ctx context.Context, imagesToDelete interface{}) {
	// Create a worker pool for parallel deletion
	pool := workerpool.New(gcr.concurrency)

	var images []*manifestStruct

	images = imagesToDelete.([]*manifestStruct)
	var deleted = make([]string, 0, len(images))
	var deletedLock sync.Mutex
	var errs = make(map[string]error)
	var errsLock sync.RWMutex
	for _, imageToDelete := range images {
		gcrrepo, err := cregistry.NewRepository(gcr.Registry.RegistryHost + "/" + imageToDelete.Repo)
		if err != nil {
			gcr.log.Fatalf("ERROR: %s", err)
		}
		for _, tag := range imageToDelete.Info.Tags {
			gcr.log.Debug("deleting tag",
				"repo", imageToDelete.Repo,
				"digest", imageToDelete.Digest,
				"tag", tag)

			tagged := gcrrepo.Tag(tag)

			if err := gcr.deleteOne(ctx, tagged); err != nil {
				gcr.log.Fatalf("failed to delete %s: %w", tagged, err)
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

			gcr.log.Debug("deleting digest",
				"repo", imageToDelete.Repo,
				"digest", imageToDelete.Digest)

			if err := gcr.deleteOne(ctx, ref); err != nil {
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
			gcr.log.Fatalf(errStrings[0])
		}

		gcr.log.Fatalf("%d errors occurred: %s", len(errStrings), strings.Join(errStrings, ", "))
	}

	sort.Strings(deleted)

}

func (gcr *service) Clean(ctx context.Context) {
	registry, err := cregistry.NewRegistry(gcr.Registry.RegistryHost)
	if err != nil {
		gcr.log.Fatalf("ERROR: %s", err)
	}

	repos, err := remote.Catalog(ctx, registry, remote.WithAuth(gcr.Auth))
	if err != nil {
		gcr.log.Fatalf("ERROR: %s", err)
	}

	imageFilter, err := regexp.Compile(gcr.Registry.OmitImagesRegex)
	if err != nil {
		gcr.log.Fatalf("ERROR: %s", err)
	}

	tagFilter, err := regexp.Compile(gcr.Registry.OmitTagsRegex)
	if err != nil {
		gcr.log.Fatalf("ERROR: %s", err)
	}

	since := time.Now().AddDate(0, 0, gcr.Registry.AgeDays)

	clusterImages := gcr.GetKubernetesImages(ctx)

	// imagesToDelete := []string{}
	var imagesToDelete []*manifestStruct

	gcr.log.Info("Checking for deletion candidates...")
	for _, repo := range repos {
		var repoManifests []*manifestStruct
		if strings.Contains(repo, gcr.Registry.ProjectID) {
			gcrrepo, err := cregistry.NewRepository(gcr.Registry.RegistryHost + "/" + repo)
			if err != nil {
				gcr.log.Fatalf("ERROR: %s", err)
			}

			imageinfo, err := google.List(gcrrepo,
				google.WithContext(ctx),
				google.WithAuth(gcr.Auth))
			if err != nil {
				gcr.log.Fatalf("ERROR: %s", err)
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
					to_delete := gcr.shouldDelete(manifest, since, tagFilter, imageFilter, clusterImages)
					if to_delete {
						gcr.log.Info("uploaded time: " + manifest.Info.Uploaded.String() + " image: " + gcr.Registry.RegistryHost + "/" + repo + "@" + manifest.Digest + " with tags " + strings.Join(manifest.Info.Tags, ","))
						imagesToDelete = append(imagesToDelete, &manifestStruct{repo, manifest.Digest, manifest.Info})
					}
				}
			}

		}

	}

	if len(imagesToDelete) == 0 {
		gcr.log.Info("There are no images to be deleted")
		return
	}
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Press y to delete  " + strconv.Itoa(len(imagesToDelete)) + " images listed above :")
	text, _ := reader.ReadString('\n')

	if strings.ToLower(text) == "y\n" {
		gcr.Delete(ctx, imagesToDelete)
		gcr.log.Info("Successful deletion of " + strconv.Itoa(len(imagesToDelete)) + " images")
	}

}
