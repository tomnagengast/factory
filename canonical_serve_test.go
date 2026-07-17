package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/server"
)

func TestCanonicalBootstrapExposesOnlyExactHealthIdentity(t *testing.T) {
	t.Parallel()
	build := server.BuildIdentity{
		Commit: "commit", Tree: "tree", BuildID: "build", DeploymentID: "deployment",
		ContractVersion: "1", StartedAt: time.Date(2026, time.July, 17, 17, 0, 0, 0, time.UTC),
	}
	handler := canonicalBootstrapHandler(build)
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	var response struct {
		Status string `json:"status"`
		App    string `json:"app"`
		server.BuildIdentity
	}
	if err := json.NewDecoder(health.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "ok" || response.App != "factory" || response.BuildIdentity != build {
		t.Fatalf("health identity = %+v", response)
	}
	gated := httptest.NewRecorder()
	handler.ServeHTTP(gated, httptest.NewRequest(http.MethodGet, "/api/home", nil))
	if gated.Code != http.StatusServiceUnavailable || gated.Header().Get("Retry-After") != "2" {
		t.Fatalf("gated status=%d headers=%v", gated.Code, gated.Header())
	}
}

func TestCanonicalCompiledRepositoriesPreserveManagedAndDeploymentIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	stateRoot := filepath.Join(home, ".local", "share", "factory")
	managed := filepath.Join(home, "repos", "tomnagengast")
	network := filepath.Join(stateRoot, "workspace", "network")
	values := canonicalCompiledRepositories(home, stateRoot, network, managed, "http://127.0.0.1:8092/api/healthz")
	if len(values) != 4 || values[0].Repository != "tomnagengast/network" || values[0].RepoPath != network ||
		values[2].Repository != "tomnagengast/factory" || values[2].ReceiptPath != filepath.Join(stateRoot, "deployments", "current.json") ||
		!values[3].Bootstrap {
		t.Fatalf("compiled repositories = %+v", values)
	}
}
