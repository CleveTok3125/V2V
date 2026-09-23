package main

import (
	"time"

	"github.com/CleveTok3125/V2V/internal/serverconfig"
)

// envLoader is an alias of the shared loader so server tests exercise the
// same parser the production loaders use.
type envLoader = serverconfig.EnvLoader

// The helpers below are thin wrappers over the shared package, retained for
// callers that need a single value without a warning channel.
func getEnvAsInt(key string) (int, error) { return serverconfig.GetEnvAsInt(key) }

func getEnvAsDuration(key string) (time.Duration, error) {
	return serverconfig.GetEnvAsDuration(key)
}

func getEnvFallback(key string, fallback string) string {
	return serverconfig.GetEnvFallback(key, fallback)
}

func getEnvAsBoolFallback(key string, fallback bool) bool {
	return serverconfig.GetEnvAsBoolFallback(key, fallback)
}
