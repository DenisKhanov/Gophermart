package config

import (
	"flag"
	"github.com/caarlos0/env"
	"github.com/sirupsen/logrus"
)

// ENVConfig holds configuration settings extracted from environment variables.
// This struct is used to configure various aspects of the application.
type ENVConfig struct {
	EnvServAdr              string `env:"RUN_ADDRESS"`
	EnvAccrualSystemAddress string `env:"ACCRUAL_SYSTEM_ADDRESS"`
	EnvDataBase             string `env:"DATABASE_URI"`
	EnvLogLevel             string `env:"LOG_LEVEL"`
}

func NewConfig() *ENVConfig {
	var cfg ENVConfig

	flag.StringVar(&cfg.EnvServAdr, "a", "localhost:8090", "HTTP server address")

	flag.StringVar(&cfg.EnvAccrualSystemAddress, "r", "http://localhost:8080", "Set URL accrual_system address")

	flag.StringVar(&cfg.EnvLogLevel, "l", "info", "Set logg level")

	flag.StringVar(&cfg.EnvDataBase, "d", "user=postgres password=12121212 dbname=gophermart port=5433 sslmode=disable", "Set connect dbPool config")

	flag.Parse()

	err := env.Parse(&cfg)
	if err != nil {
		logrus.Fatal(err)
	}

	return &cfg
}
