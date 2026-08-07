package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"
	"github.com/tomnagengast/factory/internal/githubauth"
)

const (
	defaultListenAddress = "127.0.0.1:8787"
	defaultGitHubAPIURL  = "https://api.github.com"
)

type config struct {
	appID          int64
	installationID int64
	privateKey     []byte
	brokerSecret   []byte
	listenAddress  string
	githubAPIURL   string
	tokenOptions   *github.InstallationTokenOptions
	tlsCertificate string
	tlsPrivateKey  string
}

type tokenProvider interface {
	Token(context.Context) (string, error)
	Expiry() (expiresAt time.Time, refreshAt time.Time, err error)
}

type tokenHandler struct {
	secret   []byte
	provider tokenProvider
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("github-token-broker", flag.ContinueOnError)
	listenAddress := flags.String("listen", "", "HTTP listen address")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("github-token-broker accepts no positional arguments")
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if *listenAddress != "" {
		cfg.listenAddress = *listenAddress
	}

	transport, err := ghinstallation.New(http.DefaultTransport, cfg.appID, cfg.installationID, cfg.privateKey)
	if err != nil {
		return fmt.Errorf("configure GitHub App signer: %w", err)
	}
	transport.BaseURL = cfg.githubAPIURL
	transport.InstallationTokenOptions = cfg.tokenOptions

	handler := newHandler(cfg.brokerSecret, transport)
	server := &http.Server{
		Addr:              cfg.listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErrors := make(chan error, 1)
	go func() {
		log.Printf("GitHub token broker listening on %s", cfg.listenAddress)
		if cfg.tlsCertificate != "" {
			serveErrors <- server.ListenAndServeTLS(cfg.tlsCertificate, cfg.tlsPrivateKey)
			return
		}
		serveErrors <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-signals:
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}

func newHandler(secret []byte, provider tokenProvider) http.Handler {
	broker := &tokenHandler{secret: secret, provider: provider}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", broker.health)
	mux.HandleFunc("/token", broker.token)
	return mux
}

func (h *tokenHandler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
}

func (h *tokenHandler) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if provided == r.Header.Get("Authorization") || subtle.ConstantTimeCompare([]byte(provided), h.secret) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	token, err := h.provider.Token(r.Context())
	if err != nil {
		log.Printf("GitHub installation token request failed: %v", err)
		http.Error(w, "token unavailable", http.StatusBadGateway)
		return
	}
	expiresAt, _, err := h.provider.Expiry()
	if err != nil {
		log.Printf("GitHub installation token expiry unavailable: %v", err)
		http.Error(w, "token unavailable", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(tokenResponse{Token: token, ExpiresAt: expiresAt}); err != nil {
		log.Printf("write token response: %v", err)
	}
}

func loadConfig() (config, error) {
	appID, err := positiveInt64("GITHUB_APP_ID")
	if err != nil {
		return config{}, err
	}
	installationID, err := positiveInt64("GITHUB_APP_INSTALLATION_ID")
	if err != nil {
		return config{}, err
	}
	privateKey, err := readPrivateKey()
	if err != nil {
		return config{}, err
	}
	brokerSecret, err := githubauth.ReadSecret("GITHUB_TOKEN_BROKER_SECRET_FILE", "GITHUB_TOKEN_BROKER_SECRET")
	if err != nil {
		return config{}, err
	}
	tokenOptions, err := parseTokenOptions(os.Getenv("GITHUB_APP_REPOSITORIES"), os.Getenv("GITHUB_APP_PERMISSIONS"))
	if err != nil {
		return config{}, err
	}

	listenAddress := strings.TrimSpace(os.Getenv("GITHUB_TOKEN_BROKER_LISTEN"))
	if listenAddress == "" {
		listenAddress = defaultListenAddress
	}
	githubAPIURL := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if githubAPIURL == "" {
		githubAPIURL = defaultGitHubAPIURL
	}
	if err := validateGitHubAPIURL(githubAPIURL); err != nil {
		return config{}, err
	}

	tlsCertificate := strings.TrimSpace(os.Getenv("GITHUB_TOKEN_BROKER_TLS_CERT_FILE"))
	tlsPrivateKey := strings.TrimSpace(os.Getenv("GITHUB_TOKEN_BROKER_TLS_KEY_FILE"))
	if (tlsCertificate == "") != (tlsPrivateKey == "") {
		return config{}, errors.New("set both GITHUB_TOKEN_BROKER_TLS_CERT_FILE and GITHUB_TOKEN_BROKER_TLS_KEY_FILE")
	}

	return config{
		appID:          appID,
		installationID: installationID,
		privateKey:     privateKey,
		brokerSecret:   brokerSecret,
		listenAddress:  listenAddress,
		githubAPIURL:   strings.TrimRight(githubAPIURL, "/"),
		tokenOptions:   tokenOptions,
		tlsCertificate: tlsCertificate,
		tlsPrivateKey:  tlsPrivateKey,
	}, nil
}

func positiveInt64(name string) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func readPrivateKey() ([]byte, error) {
	path := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"))
	encoded, encodedSet := os.LookupEnv("GITHUB_APP_PRIVATE_KEY_BASE64")
	encodedSet = encodedSet && strings.TrimSpace(encoded) != ""
	if path != "" && encodedSet {
		return nil, errors.New("set only one of GITHUB_APP_PRIVATE_KEY_FILE and GITHUB_APP_PRIVATE_KEY_BASE64")
	}
	if path != "" {
		privateKey, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
		}
		return privateKey, nil
	}
	if !encodedSet {
		return nil, errors.New("set GITHUB_APP_PRIVATE_KEY_FILE or GITHUB_APP_PRIVATE_KEY_BASE64")
	}
	privateKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, errors.New("GITHUB_APP_PRIVATE_KEY_BASE64 is not valid base64")
	}
	return privateKey, nil
}

func parseTokenOptions(repositoryValue, permissionValue string) (*github.InstallationTokenOptions, error) {
	repositoryValue = strings.TrimSpace(repositoryValue)
	permissionValue = strings.TrimSpace(permissionValue)
	if repositoryValue == "" {
		return nil, errors.New("GITHUB_APP_REPOSITORIES is required; use a comma-separated list or explicit *")
	}
	if permissionValue == "" {
		return nil, errors.New("GITHUB_APP_PERMISSIONS is required; use a JSON object or explicit *")
	}

	options := &github.InstallationTokenOptions{}
	if repositoryValue != "*" {
		seen := make(map[string]struct{})
		for _, raw := range strings.Split(repositoryValue, ",") {
			repository := strings.TrimSpace(raw)
			if repository == "" || strings.Contains(repository, "/") {
				return nil, errors.New("GITHUB_APP_REPOSITORIES must contain repository names without owners")
			}
			if _, exists := seen[repository]; exists {
				continue
			}
			seen[repository] = struct{}{}
			options.Repositories = append(options.Repositories, repository)
		}
		if len(options.Repositories) == 0 {
			return nil, errors.New("GITHUB_APP_REPOSITORIES must contain at least one repository")
		}
	}

	if permissionValue != "*" {
		var values map[string]string
		decoder := json.NewDecoder(strings.NewReader(permissionValue))
		if err := decoder.Decode(&values); err != nil {
			return nil, errors.New("GITHUB_APP_PERMISSIONS must be a JSON object")
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, errors.New("GITHUB_APP_PERMISSIONS must contain one JSON object")
		}
		if len(values) == 0 {
			return nil, errors.New("GITHUB_APP_PERMISSIONS must not be empty")
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if values[key] != "read" && values[key] != "write" {
				return nil, fmt.Errorf("GITHUB_APP_PERMISSIONS %q must be read or write", key)
			}
		}

		encoded, _ := json.Marshal(values)
		permissions := new(github.InstallationPermissions)
		permissionDecoder := json.NewDecoder(strings.NewReader(string(encoded)))
		permissionDecoder.DisallowUnknownFields()
		if err := permissionDecoder.Decode(permissions); err != nil {
			return nil, fmt.Errorf("GITHUB_APP_PERMISSIONS contains an unknown permission: %w", err)
		}
		options.Permissions = permissions
	}

	if repositoryValue == "*" && permissionValue == "*" {
		return nil, nil
	}
	return options, nil
}

func validateGitHubAPIURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("GITHUB_API_URL must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Scheme != "https" {
		return errors.New("GITHUB_API_URL must use HTTPS")
	}
	return nil
}
