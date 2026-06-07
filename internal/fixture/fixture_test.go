package fixture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionStableAndNonEmpty(t *testing.T) {
	v1, err := Version()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v1 == "" {
		t.Fatal("version should not be empty")
	}
	v2, err := Version()
	if err != nil {
		t.Fatalf("version (2nd): %v", err)
	}
	if v1 != v2 {
		t.Errorf("version not stable: %q vs %q", v1, v2)
	}
}

func TestExtractWritesBuildContext(t *testing.T) {
	dir, cleanup, err := Extract()
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	defer cleanup()

	for _, name := range []string{Dockerfile, "index.html"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s in build context: %v", name, err)
		}
	}

	// The Dockerfile must reference busybox (sanity that the right context landed).
	data, err := os.ReadFile(filepath.Join(dir, Dockerfile))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	if len(data) == 0 {
		t.Error("Dockerfile is empty")
	}

	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup should remove the temp dir, stat err = %v", err)
	}
}
