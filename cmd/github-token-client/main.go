package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tomnagengast/factory/internal/githubauth"
)

const defaultGitHost = "github.com"

type config struct {
	brokerURL string
	secret    []byte
	gitHost   string
}

type brokerClient struct {
	url        string
	secret     []byte
	httpClient *http.Client
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, stdin io.Reader, stdout io.Writer) error {
	if len(arguments) == 0 {
		return usageError()
	}

	configuration, err := loadConfig()
	if err != nil {
		return err
	}
	client := &brokerClient{
		url:        configuration.brokerURL,
		secret:     configuration.secret,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}

	switch arguments[0] {
	case "get":
		return credentialGet(context.Background(), client, configuration.gitHost, stdin, stdout)
	case "store", "erase", "capability":
		return nil
	case "exec":
		command := arguments[1:]
		if len(command) > 0 && command[0] == "--" {
			command = command[1:]
		}
		if len(command) == 0 {
			return errors.New("exec requires a command")
		}
		token, err := client.token(context.Background())
		if err != nil {
			return err
		}
		return execWithToken(command, token.Token)
	case "check":
		token, err := client.token(context.Background())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "GitHub token broker ready; token expires %s\n", token.ExpiresAt.UTC().Format(time.RFC3339))
		return err
	case "configure-git":
		return configureGit(configuration.gitHost)
	default:
		return usageError()
	}
}

func usageError() error {
	return errors.New("usage: github-token-client <get|store|erase|capability|exec|check|configure-git>")
}

func loadConfig() (config, error) {
	brokerURL := strings.TrimSpace(os.Getenv("GITHUB_TOKEN_BROKER_URL"))
	if brokerURL == "" {
		return config{}, errors.New("GITHUB_TOKEN_BROKER_URL is required")
	}
	allowHTTP := envBool("GITHUB_TOKEN_BROKER_ALLOW_HTTP")
	parsed, err := url.Parse(brokerURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return config{}, errors.New("GITHUB_TOKEN_BROKER_URL must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return config{}, errors.New("GITHUB_TOKEN_BROKER_URL must not contain a path")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return config{}, errors.New("GITHUB_TOKEN_BROKER_URL must use HTTPS; set GITHUB_TOKEN_BROKER_ALLOW_HTTP=true only on a private local network")
	}

	secret, err := githubauth.ReadSecret("GITHUB_TOKEN_BROKER_SECRET_FILE", "GITHUB_TOKEN_BROKER_SECRET")
	if err != nil {
		return config{}, err
	}
	gitHost := strings.TrimSpace(os.Getenv("GITHUB_TOKEN_BROKER_GIT_HOST"))
	if gitHost == "" {
		gitHost = defaultGitHost
	}
	if err := validateGitHost(gitHost); err != nil {
		return config{}, err
	}

	parsed.Path = "/token"
	return config{brokerURL: parsed.String(), secret: secret, gitHost: gitHost}, nil
}

func (c *brokerClient) token(ctx context.Context) (tokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, nil)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("create token request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+string(c.secret))
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("request GitHub token: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("read token response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("token broker returned %s", response.Status)
	}

	var token tokenResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&token); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return tokenResponse{}, errors.New("decode token response: expected one JSON object")
	}
	if token.Token == "" || token.ExpiresAt.Before(time.Now().Add(30*time.Second)) {
		return tokenResponse{}, errors.New("token broker returned an empty or expiring token")
	}
	return token, nil
}

func credentialGet(ctx context.Context, client *brokerClient, gitHost string, input io.Reader, output io.Writer) error {
	fields, err := readCredentialRequest(input)
	if err != nil {
		return err
	}
	if fields["protocol"] != "https" || !sameGitHost(fields["host"], gitHost) {
		return nil
	}

	token, err := client.token(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "username=x-access-token\npassword=%s\n\n", token.Token)
	return err
}

func readCredentialRequest(input io.Reader) (map[string]string, error) {
	fields := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(input, 64<<10))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, found := strings.Cut(line, "=")
		if found {
			fields[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Git credential request: %w", err)
	}
	return fields, nil
}

func sameGitHost(value, configured string) bool {
	parsed, err := url.Parse("https://" + value)
	return err == nil && strings.EqualFold(parsed.Hostname(), configured)
}

func validateGitHost(host string) error {
	parsed, err := url.Parse("https://" + host)
	if err != nil || parsed.Hostname() != host || parsed.Port() != "" || parsed.Path != "" || parsed.User != nil {
		return errors.New("GITHUB_TOKEN_BROKER_GIT_HOST must be a hostname")
	}
	return nil
}

func configureGit(host string) error {
	credentialKey := fmt.Sprintf("credential.https://%s.helper", host)
	insteadOfKey := fmt.Sprintf("url.https://%s/.insteadOf", host)
	commands := [][]string{
		// An empty helper resets lower-priority helpers before the app helper.
		// This prevents a generic personal credential from satisfying a GitHub
		// request first when a home directory is reused.
		{"config", "--global", "--replace-all", credentialKey, ""},
		{"config", "--global", "--add", credentialKey, "github-app"},
		{"config", "--global", "--replace-all", insteadOfKey, fmt.Sprintf("git@%s:", host)},
		{"config", "--global", "--add", insteadOfKey, fmt.Sprintf("ssh://git@%s/", host)},
	}
	for _, arguments := range commands {
		command := exec.Command("git", arguments...)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %s: %w", strings.Join(arguments, " "), strings.TrimSpace(string(output)), err)
		}
	}
	return nil
}

func execWithToken(command []string, token string) error {
	executable, err := exec.LookPath(command[0])
	if err != nil {
		return err
	}
	environment := withToken(os.Environ(), token)
	return syscall.Exec(executable, command, environment)
}

func withToken(environment []string, token string) []string {
	result := make([]string, 0, len(environment)+2)
	for _, value := range environment {
		if strings.HasPrefix(value, "GH_TOKEN=") || strings.HasPrefix(value, "GITHUB_TOKEN=") {
			continue
		}
		result = append(result, value)
	}
	return append(result, "GH_TOKEN="+token, "GITHUB_TOKEN="+token)
}

func envBool(name string) bool {
	value, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return value
}
