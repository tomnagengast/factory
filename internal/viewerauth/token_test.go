package viewerauth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomnagengast/factory/internal/taskstore"
)

const testAPIToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestNewTokenRequiresDelegateAndStrongToken(t *testing.T) {
	t.Parallel()
	inner := testAuthenticator(t, nil)
	if _, err := NewToken(nil, testAPIToken); err == nil {
		t.Fatal("nil delegate should be rejected")
	}
	for _, invalid := range []string{"", "short", strings.Repeat("a", tokenMaxBytes+1), "has space" + strings.Repeat("a", 32), strings.Repeat("a", 31) + "\n"} {
		if _, err := NewToken(inner, invalid); err == nil {
			t.Fatalf("token %q should be rejected", invalid)
		}
	}
	if _, err := NewToken(inner, testAPIToken); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
}

func TestTokenAuthorizesAPIOnly(t *testing.T) {
	t.Parallel()
	auth, err := NewToken(testAuthenticator(t, nil), testAPIToken)
	if err != nil {
		t.Fatalf("new token: %v", err)
	}

	ran := false
	api := auth.API(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true }))

	valid := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	request.Header.Set("Authorization", "Bearer "+testAPIToken)
	api.ServeHTTP(valid, request)
	if valid.Code != http.StatusOK || !ran {
		t.Fatalf("bearer API request = %d, ran = %v", valid.Code, ran)
	}
	if valid.Header().Get("X-Frame-Options") != "DENY" || valid.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("bearer API headers = %v", valid.Header())
	}

	ran = false
	wrong := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("f", 64))
	api.ServeHTTP(wrong, request)
	if wrong.Code != http.StatusUnauthorized || ran {
		t.Fatalf("wrong bearer = %d, ran = %v", wrong.Code, ran)
	}

	missing := httptest.NewRecorder()
	api.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer = %d", missing.Code)
	}

	page := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/agents", nil)
	request.Header.Set("Authorization", "Bearer "+testAPIToken)
	auth.Page(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("token must not authorize pages")
	})).ServeHTTP(page, request)
	if page.Code != http.StatusFound {
		t.Fatalf("bearer page request = %d, want redirect to login", page.Code)
	}
}

func TestTokenActorDelegatesWithoutBearer(t *testing.T) {
	t.Parallel()
	auth, err := NewToken(testAuthenticator(t, nil), testAPIToken)
	if err != nil {
		t.Fatalf("new token: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/tasks", nil)
	request.Header.Set("Authorization", "Bearer "+testAPIToken)
	actor, ok := auth.Actor(request)
	if !ok || actor.ID != "token:local-operator" || actor.Kind != taskstore.AuthorHuman {
		t.Fatalf("bearer actor = %+v ok=%v", actor, ok)
	}

	if _, ok := auth.Actor(httptest.NewRequest(http.MethodPost, "/api/tasks", nil)); ok {
		t.Fatal("actor without bearer or session should delegate to unauthenticated inner")
	}
}

func TestLoadOrCreateToken(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "data", "api-token")

	created, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if len(created) != tokenFileBytes*2 || !validToken(created) {
		t.Fatalf("created token %q is invalid", created)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, err %v", info.Mode(), err)
	}

	loaded, err := LoadOrCreateToken(path)
	if err != nil || loaded != created {
		t.Fatalf("reload token = %q, err %v", loaded, err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := LoadOrCreateToken(path); err == nil {
		t.Fatal("permissive token file should be rejected")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatalf("write invalid token: %v", err)
	}
	if _, err := LoadOrCreateToken(path); err == nil {
		t.Fatal("invalid stored token should be rejected")
	}
}
