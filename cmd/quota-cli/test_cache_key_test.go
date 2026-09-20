package main

import (
	"testing"

	"github.com/sky1core/quota/internal/config"
)

func quotaTestCacheKey(t *testing.T, provider, dir string) string {
	t.Helper()
	return provider + ":" + quotaTestAccountDir(t, dir)
}

func quotaTestAccountDir(t *testing.T, dir string) string {
	t.Helper()
	canonical, err := config.CanonicalAccountDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
