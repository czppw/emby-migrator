package config

import "os"

type Config struct {
	ListenAddr    string
	DataDir       string
	ConfigDir     string
	Version       string
	AdminPassword string
	SessionSecret string
}

func FromEnv() Config {
	return Config{
		ListenAddr:    env("EMBY_MIGRATOR_ADDR", ":8787"),
		DataDir:       env("EMBY_MIGRATOR_DATA", "/data"),
		ConfigDir:     env("EMBY_MIGRATOR_CONFIG", "/config"),
		Version:       env("EMBY_MIGRATOR_VERSION", "0.1.0-beta.1"),
		AdminPassword: env("EMBY_MIGRATOR_PASSWORD", "password"),
		SessionSecret: os.Getenv("EMBY_MIGRATOR_SESSION_SECRET"),
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
