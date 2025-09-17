package server

import "github.com/gaborage/go-bricks/config"

const envAliasDev = "dev"

func isDevelopmentEnv(env string) bool {
	return env == config.EnvDevelopment || env == envAliasDev
}
