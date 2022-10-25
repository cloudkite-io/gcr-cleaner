package app

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"k8s.io/utils/strings/slices"
	"log"
	"os"
)

var registryStrs = []string{"gcr", "ecr", "docker"}

type Config struct {
	Registries []Registry `yaml:"registries"`
}

type Registry struct {
	Concurrency        int      `yaml:"concurrency"`
	AgeDays            int      `yaml:"age_days"`
	KubernetesContexts []string `yaml:"kubernetes_contexts"`
	Name               string   `yaml:"name"`
	OmitImagesRegex    string   `yaml:"omit_images_regex"`
	OmitTagsRegex      string   `yaml:"omit_tags_regex"`
	Organization       string   `yaml:"organization"`
	ProjectID          string   `yaml:"project_id"`
	RegistryHost       string   `yaml:"registry_host"`
}

func LoadConfig(registry string) Registry {
	if registry == "" {
		log.Println("must specify registry with --registry")
		return Registry{}
	}
	f, err := os.Open("config.yaml")
	if err != nil {
		log.Println("couldn't open file config.yaml")
		panic(err)
	}

	if !slices.Contains(registryStrs, registry) {
		fmt.Fprintf(os.Stderr, "invalid registry name: %s\n", registry)
		os.Exit(2) // the same exit code flag.Parse uses
	}

	var c Config
	dec := yaml.NewDecoder(f)
	err = dec.Decode(&c)
	if err != nil {
		panic(err)
	}

	for _, r := range c.Registries {
		if r.Name == registry {
			return r
		}
	}

	return Registry{}
}
