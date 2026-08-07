package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrokerClientAndCredentialHelper(t *testing.T) {
	secret := strings.Repeat("s", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Fatalf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{Token: "installation-token", ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer server.Close()

	client := &brokerClient{url: server.URL + "/token", secret: []byte(secret), httpClient: server.Client()}
	var output strings.Builder
	err := credentialGet(
		context.Background(),
		client,
		"github.com",
		strings.NewReader("protocol=https\nhost=github.com\n\n"),
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "username=x-access-token\npassword=installation-token\n\n"; got != want {
		t.Fatalf("credential output = %q, want %q", got, want)
	}
}

func TestCredentialHelperIgnoresOtherHosts(t *testing.T) {
	client := &brokerClient{httpClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected token request")
		return nil, nil
	})}}
	var output strings.Builder
	err := credentialGet(context.Background(), client, "github.com", strings.NewReader("protocol=https\nhost=example.com\n\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q", output.String())
	}
}

func TestLoadConfigRequiresExplicitHTTPAndSecret(t *testing.T) {
	t.Setenv("GITHUB_TOKEN_BROKER_URL", "http://broker:8787")
	t.Setenv("GITHUB_TOKEN_BROKER_SECRET", strings.Repeat("s", 32))
	if _, err := loadConfig(); err == nil {
		t.Fatal("accepted HTTP without opt-in")
	}
	t.Setenv("GITHUB_TOKEN_BROKER_ALLOW_HTTP", "true")
	configuration, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.brokerURL != "http://broker:8787/token" {
		t.Fatalf("broker URL = %q", configuration.brokerURL)
	}
}

func TestConfigureGitUsesScopedHelperAndHTTPSRewrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seed := exec.Command("git", "config", "--global", "credential.helper", "personal-helper")
	if output, err := seed.CombinedOutput(); err != nil {
		t.Fatalf("seed git config: %s: %v", output, err)
	}
	if err := configureGit("github.com"); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(contents)
	for _, want := range []string{"helper =", "helper = github-app", "insteadOf = git@github.com:", "insteadOf = ssh://git@github.com/"} {
		if !strings.Contains(configuration, want) {
			t.Fatalf("git config missing %q:\n%s", want, configuration)
		}
	}
	helper := exec.Command("git", "config", "--global", "--get-all", "credential.https://github.com.helper")
	output, err := helper.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(output), "\ngithub-app\n"; got != want {
		t.Fatalf("scoped helpers = %q, want %q", got, want)
	}
}

func TestWithTokenReplacesExistingValues(t *testing.T) {
	environment := withToken([]string{"PATH=/bin", "GH_TOKEN=old", "GITHUB_TOKEN=old"}, "new")
	if got, want := strings.Join(environment, "\n"), "PATH=/bin\nGH_TOKEN=new\nGITHUB_TOKEN=new"; got != want {
		t.Fatalf("environment = %q, want %q", got, want)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
