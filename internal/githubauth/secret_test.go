package githubauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSecretFromFile(t *testing.T) {
	t.Setenv("TEST_SECRET", "")
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("  "+strings.Repeat("s", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SECRET_FILE", path)

	secret, err := ReadSecret("TEST_SECRET_FILE", "TEST_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(secret), strings.Repeat("s", 32); got != want {
		t.Fatalf("secret = %q, want %q", got, want)
	}
}

func TestReadSecretRejectsAmbiguousAndShortValues(t *testing.T) {
	t.Setenv("TEST_SECRET_FILE", "/tmp/unused")
	t.Setenv("TEST_SECRET", strings.Repeat("s", 32))
	if _, err := ReadSecret("TEST_SECRET_FILE", "TEST_SECRET"); err == nil {
		t.Fatal("ReadSecret accepted both sources")
	}

	t.Setenv("TEST_SECRET_FILE", "")
	t.Setenv("TEST_SECRET", "short")
	if _, err := ReadSecret("TEST_SECRET_FILE", "TEST_SECRET"); err == nil {
		t.Fatal("ReadSecret accepted a short secret")
	}
}
