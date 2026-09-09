package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppearancePersistenceAndInvalidFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := loadAppearance(); got != appearanceSystem {
		t.Fatalf("missing preference = %q", got)
	}
	for _, mode := range []appearanceMode{appearanceDark, appearanceLight, appearanceSystem} {
		if err := saveAppearance(mode); err != nil {
			t.Fatal(err)
		}
		if got := loadAppearance(); got != mode {
			t.Fatalf("saved %q loaded %q", mode, got)
		}
	}
	path, err := appearancePath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatal("appearance file should be owner-private")
	}
	for _, data := range []string{`{"mode":"invalid"}`, `{broken`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if got := loadAppearance(); got != appearanceSystem {
			t.Fatalf("invalid preference = %q", got)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveAppearance(appearanceDark); err == nil {
		t.Fatal("replacing a directory must fail")
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".appearance-*"))
	if len(matches) != 0 {
		t.Fatal("failed save left temporary files")
	}
}
