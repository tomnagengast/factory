package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/repositories"
	"github.com/tomnagengast/factory/internal/runs"
	"github.com/tomnagengast/factory/internal/taskmodel"
	"github.com/tomnagengast/factory/internal/taskstore"
)

type LinearProjectReader interface {
	ReadProject(context.Context, string) (projectsetup.Request, error)
}

// LinearGraphQLProjectReader reads the complete project identity needed by the
// canonical repository catalog. It does not interpret routing metadata.
type LinearGraphQLProjectReader struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

func NewLinearGraphQLProjectReader(endpoint, apiKey string, client *http.Client) (*LinearGraphQLProjectReader, error) {
	parsed, err := url.Parse(endpoint)
	if endpoint == "" || apiKey == "" || client == nil || err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("app Linear project reader: HTTPS endpoint, API key, and client are required")
	}
	return &LinearGraphQLProjectReader{endpoint: endpoint, apiKey: apiKey, client: client}, nil
}

func (r *LinearGraphQLProjectReader) ReadProject(ctx context.Context, identifier string) (projectsetup.Request, error) {
	if _, err := taskmodel.LegacyLinear(identifier); err != nil {
		return projectsetup.Request{}, permanentRoute(errors.New("resolve repository: invalid Linear task identifier"))
	}
	payload, err := json.Marshal(map[string]any{
		"query":     `query FactoryRepository($id: String!) { issue(id: $id) { project { id name description } } }`,
		"variables": map[string]string{"id": identifier},
	})
	if err != nil {
		return projectsetup.Request{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(payload))
	if err != nil {
		return projectsetup.Request{}, err
	}
	request.Header.Set("Authorization", r.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return projectsetup.Request{}, fmt.Errorf("resolve repository: Linear request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return projectsetup.Request{}, fmt.Errorf("resolve repository: Linear HTTP %d", response.StatusCode)
	}
	var value struct {
		Data struct {
			Issue *struct {
				Project *struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"project"`
			} `json:"issue"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value); err != nil {
		return projectsetup.Request{}, fmt.Errorf("resolve repository: decode Linear response: %w", err)
	}
	if len(value.Errors) > 0 {
		return projectsetup.Request{}, fmt.Errorf("resolve repository: Linear: %s", value.Errors[0].Message)
	}
	if value.Data.Issue == nil || value.Data.Issue.Project == nil {
		return projectsetup.Request{}, permanentRoute(errors.New("resolve repository: issue has no Linear project"))
	}
	project := value.Data.Issue.Project
	return projectsetup.Request{ProjectID: project.ID, ProjectName: project.Name, Description: project.Description}, nil
}

// RepositoryResolver resolves every Run through a fresh canonical catalog
// projection. Linear tasks prove live project metadata; Factory tasks prove the
// private task journal's pinned route still matches the admitted catalog.
type RepositoryResolver struct {
	store  *repositories.Store
	tasks  *taskstore.Store
	linear LinearProjectReader
	parser *projectsetup.Parser
}

func NewRepositoryResolver(store *repositories.Store, tasks *taskstore.Store, linear LinearProjectReader, parser *projectsetup.Parser) (*RepositoryResolver, error) {
	if store == nil || tasks == nil || linear == nil || parser == nil {
		return nil, errors.New("app repository resolver: canonical stores, Linear reader, and metadata parser are required")
	}
	return &RepositoryResolver{store: store, tasks: tasks, linear: linear, parser: parser}, nil
}

func (r *RepositoryResolver) ResolveRoute(ctx context.Context, run runs.Run) (repositories.Route, error) {
	ref, err := run.Causation.Task.Normalize()
	if err != nil {
		return repositories.Route{}, permanentRoute(err)
	}
	catalog, err := repositories.NewCatalog(r.store.Snapshot())
	if err != nil {
		return repositories.Route{}, err
	}
	var lookup repositories.TaskLookup
	lookup.Ref = ref
	switch ref.Source {
	case taskmodel.SourceLinear:
		request, err := r.linear.ReadProject(ctx, ref.Identifier)
		if err != nil {
			return repositories.Route{}, err
		}
		spec, complete, err := r.parser.Parse(request)
		if err != nil {
			return repositories.Route{}, err
		}
		if !complete {
			return repositories.Route{}, permanentRoute(errors.New("resolve repository: Linear project routing metadata is incomplete"))
		}
		lookup.Project = &repositories.ProjectMetadata{
			ProjectID: spec.ProjectID, ProjectName: spec.ProjectName, Repository: spec.Repository, LocalPath: spec.LocalPath,
		}
	case taskmodel.SourceFactory:
		task, found := r.tasks.Find(ref.ProviderID)
		if !found || !task.Ref.Equal(ref) || task.Routing == nil {
			return repositories.Route{}, permanentRoute(errors.New("resolve repository: Factory task has no admitted route"))
		}
		route, err := repositories.ConvertRouteSource(repositories.RouteSource{
			ProjectID: task.ProjectID, Repository: task.Routing.Repository, RepositoryURL: task.Routing.RepositoryURL,
			RepositoryPath: task.Routing.RepositoryPath, ManagedRoot: task.Routing.ManagedRoot,
			BaseBranch: task.Routing.BaseBranch, Bootstrap: task.Routing.Bootstrap, CloudURL: task.Routing.CloudURL,
		})
		if err != nil {
			return repositories.Route{}, permanentRoute(err)
		}
		lookup.ProjectID = task.ProjectID
		lookup.Route = &route
	default:
		return repositories.Route{}, permanentRoute(errors.New("resolve repository: unsupported task provider"))
	}
	record, err := catalog.ResolveTask(lookup)
	if err != nil {
		return repositories.Route{}, err
	}
	return record.Route(), nil
}

type permanentRouteError struct{ error }

func (permanentRouteError) Permanent() bool { return true }
func (e permanentRouteError) Unwrap() error { return e.error }
func permanentRoute(err error) error        { return permanentRouteError{error: err} }
