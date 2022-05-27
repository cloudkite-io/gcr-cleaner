package config

import (
	"log"

	"github.com/joeshaw/envdecode"
)

type Conf struct {
	CleanerConf cleanerConf
}

type cleanerConf struct {
	REGISTRY            string `env:"REGISTRY,required"`
	PROJECT_ID          string `env:"PROJECT_ID,required"`
	KUBERNETES_CONTEXTS string `env:"KUBERNETES_CONTEXTS,required"`
	OMIT_IMAGES_REGEX   string `env:"OMIT_IMAGES_REGEX"`
	AGE_DAYS            string `env:"AGE_DAYS",required`
}

func AppConfig() *Conf {
	var cfg Conf
	if err := envdecode.StrictDecode(&cfg); err != nil {
		log.Fatalf("ERROR: %s", err)
	}
	log.Println("Required envs loaded successfully")
	return &cfg
}
