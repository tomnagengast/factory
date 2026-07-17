package viewerauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tomnagengast/factory/internal/taskstore"
)

const (
	tokenMinBytes  = 32
	tokenMaxBytes  = 512
	tokenFileBytes = 32
)

// Delegate matches the server's viewer-authenticator surface so token access
// composes with the Google and local policies without importing the server
// package.
type Delegate interface {
	Page(http.Handler) http.Handler
	API(http.Handler) http.Handler
	Login(http.ResponseWriter, *http.Request)
	Callback(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
	Actor(*http.Request) (taskstore.Actor, bool)
}

// Token authorizes API requests that carry the configured operator bearer
// token and delegates every other decision to the wrapped authenticator.
// Pages and the login endpoints never accept the token.
type Token struct {
	inner  Delegate
	digest [sha256.Size]byte
}

func NewToken(inner Delegate, token string) (*Token, error) {
	if inner == nil {
		return nil, errors.New("viewer auth: token delegate is required")
	}
	if !validToken(token) {
		return nil, fmt.Errorf("viewer auth: API token must be %d to %d visible ASCII bytes", tokenMinBytes, tokenMaxBytes)
	}
	return &Token{inner: inner, digest: sha256.Sum256([]byte(token))}, nil
}

func (t *Token) Page(next http.Handler) http.Handler { return t.inner.Page(next) }

func (t *Token) API(next http.Handler) http.Handler {
	delegated := t.inner.API(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t.authorized(r) {
			w.Header().Set("Cache-Control", "no-store")
			secureHeaders(w)
			next.ServeHTTP(w, r)
			return
		}
		delegated.ServeHTTP(w, r)
	})
}

func (t *Token) Login(w http.ResponseWriter, r *http.Request)    { t.inner.Login(w, r) }
func (t *Token) Callback(w http.ResponseWriter, r *http.Request) { t.inner.Callback(w, r) }
func (t *Token) Logout(w http.ResponseWriter, r *http.Request)   { t.inner.Logout(w, r) }

func (t *Token) Actor(r *http.Request) (taskstore.Actor, bool) {
	if t.authorized(r) {
		return taskstore.Actor{ID: "token:local-operator", Kind: taskstore.AuthorHuman}, true
	}
	return t.inner.Actor(r)
}

func (t *Token) authorized(r *http.Request) bool {
	scheme, value, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	value = strings.TrimSpace(value)
	if !validToken(value) {
		return false
	}
	digest := sha256.Sum256([]byte(value))
	return hmac.Equal(digest[:], t.digest[:])
}

func validToken(value string) bool {
	if len(value) < tokenMinBytes || len(value) > tokenMaxBytes {
		return false
	}
	for _, character := range value {
		if character <= ' ' || character > '~' {
			return false
		}
	}
	return true
}

// LoadOrCreateToken returns the operator API token stored at path, creating a
// random one with private permissions on first use.
func LoadOrCreateToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", fmt.Errorf("viewer auth: stat API token: %w", statErr)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return "", errors.New("viewer auth: API token file must be a regular 0600 file")
		}
		token := strings.TrimSpace(string(data))
		if !validToken(token) {
			return "", errors.New("viewer auth: stored API token is invalid")
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("viewer auth: read API token: %w", err)
	}

	value := make([]byte, tokenFileBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("viewer auth: generate API token: %w", err)
	}
	token := hex.EncodeToString(value)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("viewer auth: prepare API token directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".api-token-*")
	if err != nil {
		return "", fmt.Errorf("viewer auth: create API token: %w", err)
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return "", fmt.Errorf("viewer auth: protect API token: %w", err)
	}
	if _, err := temp.WriteString(token + "\n"); err != nil {
		temp.Close()
		return "", fmt.Errorf("viewer auth: write API token: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", fmt.Errorf("viewer auth: sync API token: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("viewer auth: close API token: %w", err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return "", fmt.Errorf("viewer auth: store API token: %w", err)
	}
	return token, nil
}
