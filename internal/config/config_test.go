package config

import "testing"

func TestFromEnvMaxConcurrency(t *testing.T) {
	t.Setenv("EMBY_MIGRATOR_MAX_CONCURRENCY", "128")
	if got := FromEnv().MaxConcurrency; got != 128 {
		t.Fatalf("MaxConcurrency = %d, want 128", got)
	}
}

func TestFromEnvMaxConcurrencyFallback(t *testing.T) {
	t.Setenv("EMBY_MIGRATOR_MAX_CONCURRENCY", "invalid")
	if got := FromEnv().MaxConcurrency; got != 64 {
		t.Fatalf("MaxConcurrency = %d, want 64", got)
	}
}
