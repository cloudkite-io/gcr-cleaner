package main

import (
	"context"
	"flag"
	app "github.com/cloudkite-io/gcr-cleaner"
	"github.com/cloudkite-io/gcr-cleaner/pkg/cleaner"
	"github.com/cloudkite-io/gcr-cleaner/pkg/utils"
)

func main() {
	strPtr := flag.String("registry", "", "pass in the registry name e.g. gcr or ecr")
	flag.Parse()
	utils.ValidateRegistryName()

	registry := app.LoadConfig(*strPtr)
	c := cleaner.NewCleaner(registry)
	ctx := context.Background()
	c.Clean(ctx)
}
