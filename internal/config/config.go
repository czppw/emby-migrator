package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr            string
	DataDir               string
	ConfigDir             string
	ImportRoot            string
	EmbyDatabaseRoot      string
	DockerHost            string
	Version               string
	AdminPassword         string
	SessionSecret         string
	MaxMemoryLogEntries   int
	MaxCompletedJobs      int
	CompletedJobRetention time.Duration
	ReleaseMemoryOnFinish bool
}

func FromEnv() Config {
	return Config{
		ListenAddr:            env("EMBY_MIGRATOR_ADDR", ":8787"),
		DataDir:               env("EMBY_MIGRATOR_DATA", "/data"),
		ConfigDir:             env("EMBY_MIGRATOR_CONFIG", "/config"),
		ImportRoot:            env("EMBY_MIGRATOR_IMPORT_ROOT", "/imports"),
		EmbyDatabaseRoot:      strings.TrimSpace(os.Getenv("EMBY_MIGRATOR_EMBY_DB_ROOT")),
		DockerHost:            env("EMBY_MIGRATOR_DOCKER_HOST", "unix:///var/run/docker.sock"),
		Version:               env("EMBY_MIGRATOR_VERSION", "1.1.1"),
		AdminPassword:         env("EMBY_MIGRATOR_PASSWORD", "password"),
		SessionSecret:         os.Getenv("EMBY_MIGRATOR_SESSION_SECRET"),
		MaxMemoryLogEntries:   envInt("EMBY_MIGRATOR_MAX_MEMORY_LOGS", 2000),
		MaxCompletedJobs:      envInt("EMBY_MIGRATOR_MAX_COMPLETED_JOBS", 20),
		CompletedJobRetention: time.Duration(envInt("EMBY_MIGRATOR_JOB_RETENTION_HOURS", 24)) * time.Hour,
		ReleaseMemoryOnFinish: envBool("EMBY_MIGRATOR_RELEASE_MEMORY_ON_FINISH", true),
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}
