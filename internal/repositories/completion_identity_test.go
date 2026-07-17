package repositories

import (
	"path/filepath"
	"testing"
)

func TestCompletionIdentityValidateRequiresExactCatalogShape(t *testing.T) {
	t.Parallel()
	valid := CompletionIdentity{
		App: "factory", Repository: "tomnagengast/factory", RemoteURLs: remoteURLs("tomnagengast/factory"),
		Path: t.TempDir(), DefaultBranch: "main",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid identity: %v", err)
	}
	tests := map[string]func(*CompletionIdentity){
		"app":        func(value *CompletionIdentity) { value.App = "Factory" },
		"repository": func(value *CompletionIdentity) { value.Repository = "TomNagengast/Factory" },
		"remotes":    func(value *CompletionIdentity) { value.RemoteURLs = value.RemoteURLs[:1] },
		"path":       func(value *CompletionIdentity) { value.Path = "relative" },
		"branch":     func(value *CompletionIdentity) { value.DefaultBranch = "main..other" },
		"deployment": func(value *CompletionIdentity) {
			value.Deployment.ReceiptPath = filepath.Join(t.TempDir(), "receipt.json")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := valid.Clone()
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("invalid identity accepted: %#v", candidate)
			}
		})
	}
}
