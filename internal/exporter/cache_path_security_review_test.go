package exporter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"emby-migrator/internal/emby"
)

func TestImportLookupCacheRetriesFetchAfterError(t *testing.T) {
	cache := newImportLookupCache()
	fetches := 0
	fetch := func() ([]emby.Item, error) {
		fetches++
		if fetches == 1 {
			return nil, errors.New("temporary lookup failure")
		}
		return []emby.Item{{ID: "recovered"}}, nil
	}

	if _, err := cache.itemLookup(context.Background(), "movie:test", fetch); err == nil {
		t.Fatal("first lookup succeeded, want temporary error")
	}
	items, err := cache.itemLookup(context.Background(), "movie:test", fetch)
	if err != nil {
		t.Fatalf("second lookup returned error: %v", err)
	}
	if fetches != 2 {
		t.Fatalf("fetch called %d times, want a second fetch after the first error", fetches)
	}
	if len(items) != 1 || items[0].ID != "recovered" {
		t.Fatalf("second lookup returned %#v, want recovered item", items)
	}
}

func TestImportLookupCacheCachesSuccessfulResult(t *testing.T) {
	cache := newImportLookupCache()
	fetches := 0
	fetch := func() ([]emby.Item, error) {
		fetches++
		return []emby.Item{{ID: "cached"}}, nil
	}

	for attempt := 0; attempt < 2; attempt++ {
		items, err := cache.itemLookup(context.Background(), "movie:cached", fetch)
		if err != nil {
			t.Fatalf("lookup %d returned error: %v", attempt+1, err)
		}
		if len(items) != 1 || items[0].ID != "cached" {
			t.Fatalf("lookup %d returned %#v, want cached item", attempt+1, items)
		}
	}
	if fetches != 1 {
		t.Fatalf("fetch called %d times, want successful result to be cached", fetches)
	}
}

func TestSafePackagePathRejectsSymlinkOutsidePackage(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.jpg")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "poster.jpg")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symbolic links are unavailable in this environment: %v", err)
	}

	if path, err := safePackagePath(root, "poster.jpg"); err == nil {
		t.Fatalf("safePackagePath returned %q, want package escape rejection", path)
	}
}

func TestSafePackagePathAllowsNormalPathAndInternalSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "images", "poster.jpg")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("poster"), 0o600); err != nil {
		t.Fatal(err)
	}

	normalPath, err := safePackagePath(root, filepath.Join("images", "poster.jpg"))
	if err != nil {
		t.Fatalf("normal package path returned error: %v", err)
	}
	assertSameFile(t, normalPath, target)

	link := filepath.Join(root, "poster-link.jpg")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("normal path passed; symbolic links are unavailable in this environment: %v", err)
	}
	linkedPath, err := safePackagePath(root, "poster-link.jpg")
	if err != nil {
		t.Fatalf("internal symbolic link returned error: %v", err)
	}
	assertSameFile(t, linkedPath, target)
}

func assertSameFile(t *testing.T, first, second string) {
	t.Helper()
	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatalf("stat %q: %v", first, err)
	}
	secondInfo, err := os.Stat(second)
	if err != nil {
		t.Fatalf("stat %q: %v", second, err)
	}
	if !os.SameFile(firstInfo, secondInfo) {
		t.Fatalf("%q and %q do not resolve to the same file", first, second)
	}
}
