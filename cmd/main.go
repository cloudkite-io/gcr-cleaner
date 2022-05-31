package main

import (
	"context"

	"github.com/cloudkite-io/gcr-cleaner/pkg/gcrcleaner"
)

func main() {
	cleaner := gcrcleaner.GCRCleaner{}
	cleaner.InitializeConfig()
	cleaner.GetKubeConfig()
	context := context.Background()
	cleaner.Clean(context)
}
