package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const maxAPIKeyLength = 16 * 1024

var ErrInvalid = errors.New("invalid credentials")

type Values struct {
	OpenAIAPIKey    string `json:"openaiApiKey,omitempty"`
	AnthropicAPIKey string `json:"anthropicApiKey,omitempty"`
}

type Update struct {
	OpenAIAPIKey    *string
	AnthropicAPIKey *string
}

type EntryStatus struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source,omitempty"`
}

type Status struct {
	Codex  EntryStatus `json:"codex"`
	Claude EntryStatus `json:"claude"`
}

type Repository interface {
	CredentialCiphertext() ([]byte, bool, error)
	SaveCredentialCiphertext([]byte) error
}

type Store struct {
	mu         sync.RWMutex
	repository Repository
	aead       cipher.AEAD
	values     Values
}

func Open(repository Repository, encodedKey string) (*Store, error) {
	if repository == nil {
		return nil, errors.New("credential repository is required")
	}
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("FACTORY_CREDENTIALS_KEY must be a base64-encoded 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create credential encryption: %w", err)
	}
	store := &Store{repository: repository, aead: aead}
	ciphertext, found, err := repository.CredentialCiphertext()
	if err != nil {
		return nil, err
	}
	if !found {
		return store, nil
	}
	if len(ciphertext) < aead.NonceSize() {
		return nil, errors.New("stored credentials are corrupt")
	}
	plaintext, err := aead.Open(nil, ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():], nil)
	if err != nil {
		return nil, errors.New("decrypt stored credentials: FACTORY_CREDENTIALS_KEY does not match")
	}
	if err := json.Unmarshal(plaintext, &store.values); err != nil {
		return nil, fmt.Errorf("decode credentials: %w", err)
	}
	if err := validateValues(store.values); err != nil {
		return nil, fmt.Errorf("validate credentials: %w", err)
	}
	return store, nil
}

func (s *Store) Status() Status {
	s.mu.RLock()
	values := s.values
	s.mu.RUnlock()
	return Status{
		Codex:  entryStatus(values.OpenAIAPIKey, "OPENAI_API_KEY"),
		Claude: entryStatus(values.AnthropicAPIKey, "ANTHROPIC_API_KEY"),
	}
}

func (s *Store) Update(update Update) error {
	if update.OpenAIAPIKey == nil && update.AnthropicAPIKey == nil {
		return fmt.Errorf("%w: at least one API key is required", ErrInvalid)
	}
	if update.OpenAIAPIKey != nil && *update.OpenAIAPIKey == "" {
		return fmt.Errorf("%w: OpenAI API key is required", ErrInvalid)
	}
	if update.AnthropicAPIKey != nil && *update.AnthropicAPIKey == "" {
		return fmt.Errorf("%w: Anthropic API key is required", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.values
	if update.OpenAIAPIKey != nil {
		next.OpenAIAPIKey = *update.OpenAIAPIKey
	}
	if update.AnthropicAPIKey != nil {
		next.AnthropicAPIKey = *update.AnthropicAPIKey
	}
	if err := validateValues(next); err != nil {
		return err
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("create credential nonce: %w", err)
	}
	ciphertext := s.aead.Seal(nonce, nonce, encoded, nil)
	if err := s.repository.SaveCredentialCiphertext(ciphertext); err != nil {
		return err
	}
	s.values = next
	return nil
}

func (s *Store) Environment() []string {
	s.mu.RLock()
	values := s.values
	s.mu.RUnlock()
	environment := os.Environ()
	if values.OpenAIAPIKey != "" {
		environment = replaceEnvironment(environment, "OPENAI_API_KEY", values.OpenAIAPIKey)
	}
	if values.AnthropicAPIKey != "" {
		environment = replaceEnvironment(environment, "ANTHROPIC_API_KEY", values.AnthropicAPIKey)
	}
	return environment
}

func entryStatus(saved, environmentName string) EntryStatus {
	if saved != "" {
		return EntryStatus{Configured: true, Source: "saved"}
	}
	if value, found := os.LookupEnv(environmentName); found && value != "" {
		return EntryStatus{Configured: true, Source: "environment"}
	}
	return EntryStatus{}
}

func validateValues(values Values) error {
	if err := validateAPIKey("OpenAI", values.OpenAIAPIKey); err != nil {
		return err
	}
	return validateAPIKey("Anthropic", values.AnthropicAPIKey)
}

func validateAPIKey(name, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > maxAPIKeyLength || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%w: %s API key is invalid", ErrInvalid, name)
	}
	return nil
}

func replaceEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}
