package app

import "github.com/sirupsen/logrus"

type Cleaner struct {
	Log         *logrus.Logger
	Concurrency int
	Kubeconfig  string
	Config      *Config
}
