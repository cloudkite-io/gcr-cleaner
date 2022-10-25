package app

import "context"

type CleanerService interface {
	GetKubernetesImages(ctx context.Context) []string
	Delete(ctx context.Context, imagesToDelete interface{})
	Clean(ctx context.Context)
}
