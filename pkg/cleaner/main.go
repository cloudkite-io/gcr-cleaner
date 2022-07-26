package cleaner

import (
	app "github.com/cloudkite-io/gcr-cleaner"
	"github.com/cloudkite-io/gcr-cleaner/pkg/gcr"
)

func NewCleaner(registry app.Registry) app.CleanerService {
	switch registry.Name {
	case "gcr":
		return gcr.NewService(registry)
	default:
		return nil
	}
}
