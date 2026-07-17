package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/runs"
)

// CompletionOptions contains process-local completion authorities. Repository
// identity is always rebuilt from the selected canonical catalog.
type CompletionOptions struct {
	GitHubPath     string
	GitDirectory   string
	LinearURL      string
	LinearAPIKey   string
	GitPath        string
	WorktrunkPath  string
	HTTPClient     *http.Client
	TaskCompletion runs.TaskCompletionProvider
	Now            func() time.Time
}

// NewCompletionValidator builds one read-only GitHub authority and one
// completion evidence reader for every immutable repository in the selected
// catalog. There is no default repository or runtime registration fallback.
func NewCompletionValidator(store *repositories.Store, options CompletionOptions) (*runs.MechanicalCompletionValidator, error) {
	if store == nil || options.HTTPClient == nil || options.Now == nil {
		return nil, errors.New("app completion: catalog, HTTP client, and clock are required")
	}
	catalog, err := repositories.NewCatalog(store.Snapshot())
	if err != nil {
		return nil, err
	}
	identities := catalog.CompletionIdentities()
	readers := make(map[string]runs.CompletionEvidenceReader, len(identities))
	for _, identity := range identities {
		reader, err := runs.NewSystemCompletionEvidence(identity, runs.SystemCompletionOptions{
			LinearURL: options.LinearURL, GitPath: options.GitPath, WorktrunkPath: options.WorktrunkPath,
			LinearAPIKey: options.LinearAPIKey, HTTPClient: options.HTTPClient, TaskCompletion: options.TaskCompletion,
		})
		if err != nil {
			return nil, err
		}
		readers[identity.Repository] = reader
	}
	evidence, err := runs.NewRepositoryCompletionEvidence(readers)
	if err != nil {
		return nil, err
	}
	pullRequests, err := runs.NewGitHubCLI(options.GitHubPath, options.GitDirectory)
	if err != nil {
		return nil, err
	}
	return runs.NewMechanicalCompletionValidator(pullRequests, evidence, options.Now)
}
