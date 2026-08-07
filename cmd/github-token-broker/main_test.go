package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"
)

type fakeTokenProvider struct {
	token     string
	expiresAt time.Time
	err       error
}

func (p *fakeTokenProvider) Token(context.Context) (string, error) {
	return p.token, p.err
}

func (p *fakeTokenProvider) Expiry() (time.Time, time.Time, error) {
	return p.expiresAt, p.expiresAt.Add(-time.Minute), p.err
}

func TestTokenHandlerRequiresBearerSecretAndDisablesCaching(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	handler := newHandler(secret, &fakeTokenProvider{token: "installation-token", expiresAt: time.Now().Add(time.Hour)})

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/token", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	if got := unauthorized.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}

	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/token", nil)
	request.Header.Set("Authorization", "Bearer "+string(secret))
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, body = %s", authorized.Code, authorized.Body.String())
	}
	var response tokenResponse
	if err := json.NewDecoder(authorized.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Token != "installation-token" {
		t.Fatalf("token = %q", response.Token)
	}
}

func TestInstallationTokenIsScopedAndCached(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})

	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/456/access_tokens" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Fatal("missing app JWT")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var options github.InstallationTokenOptions
		if err := json.Unmarshal(body, &options); err != nil {
			t.Fatal(err)
		}
		if len(options.Repositories) != 1 || options.Repositories[0] != "factory" {
			t.Fatalf("repositories = %#v", options.Repositories)
		}
		if options.Permissions == nil || options.Permissions.Issues == nil || *options.Permissions.Issues != "write" {
			t.Fatalf("permissions = %#v", options.Permissions)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":        "scoped-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"permissions":  map[string]string{"issues": "write"},
			"repositories": []map[string]any{{"name": "factory"}},
		})
	}))
	defer upstream.Close()

	transport, err := ghinstallation.New(http.DefaultTransport, 123, 456, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	transport.BaseURL = upstream.URL
	write := "write"
	transport.InstallationTokenOptions = &github.InstallationTokenOptions{
		Repositories: []string{"factory"},
		Permissions:  &github.InstallationPermissions{Issues: &write},
	}

	for range 2 {
		token, err := transport.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if token != "scoped-token" {
			t.Fatalf("token = %q", token)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
}

func TestParseTokenOptions(t *testing.T) {
	options, err := parseTokenOptions("factory,workflow,factory", `{"contents":"write","issues":"write","pull_requests":"write"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(options.Repositories, ","), "factory,workflow"; got != want {
		t.Fatalf("repositories = %q, want %q", got, want)
	}
	if options.Permissions.PullRequests == nil || *options.Permissions.PullRequests != "write" {
		t.Fatalf("pull request permissions = %#v", options.Permissions.PullRequests)
	}

	if _, err := parseTokenOptions("", `{"issues":"write"}`); err == nil {
		t.Fatal("accepted missing repositories")
	}
	if _, err := parseTokenOptions("factory", `{}`); err == nil {
		t.Fatal("accepted empty permissions")
	}
	if _, err := parseTokenOptions("factory", `{"made_up":"read"}`); err == nil {
		t.Fatal("accepted unknown permission")
	}
	if _, err := parseTokenOptions("factory", `{"issues":"write"} {}`); err == nil {
		t.Fatal("accepted trailing JSON")
	}
	if options, err := parseTokenOptions("*", "*"); err != nil || options != nil {
		t.Fatalf("explicit full scope = %#v, %v", options, err)
	}
}
