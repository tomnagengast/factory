package credential

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

var testKey = base64.StdEncoding.EncodeToString(make([]byte, 32))

type memoryRepository struct {
	ciphertext []byte
}

func (r *memoryRepository) CredentialCiphertext() ([]byte, bool, error) {
	return append([]byte(nil), r.ciphertext...), len(r.ciphertext) != 0, nil
}

func (r *memoryRepository) SaveCredentialCiphertext(ciphertext []byte) error {
	r.ciphertext = append([]byte(nil), ciphertext...)
	return nil
}

func TestStorePersistsEncryptedKeysWithoutReturningThemInStatus(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "environment-openai")
	t.Setenv("ANTHROPIC_API_KEY", "")
	repository := &memoryRepository{}
	store, err := Open(repository, testKey)
	if err != nil {
		t.Fatal(err)
	}
	status := store.Status()
	if !status.Codex.Configured || status.Codex.Source != "environment" || status.Claude.Configured {
		t.Fatalf("initial status = %#v", status)
	}
	openAI, anthropic := "saved-openai", "saved-anthropic"
	if err := store.Update(Update{OpenAIAPIKey: &openAI, AnthropicAPIKey: &anthropic}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(repository.ciphertext), openAI) || strings.Contains(string(repository.ciphertext), anthropic) {
		t.Fatal("repository contained plaintext credentials")
	}
	status = store.Status()
	if status.Codex.Source != "saved" || status.Claude.Source != "saved" {
		t.Fatalf("saved status = %#v", status)
	}
	reopened, err := Open(repository, testKey)
	if err != nil {
		t.Fatal(err)
	}
	environment := reopened.Environment()
	if environmentValue(environment, "OPENAI_API_KEY") != openAI ||
		environmentValue(environment, "ANTHROPIC_API_KEY") != anthropic {
		t.Fatalf("credential environment did not contain saved keys")
	}
	encoded, err := json.Marshal(reopened.Status())
	if err != nil || strings.Contains(string(encoded), openAI) || strings.Contains(string(encoded), anthropic) {
		t.Fatalf("status exposed credentials: %s, %v", encoded, err)
	}
}

func TestStoreRejectsInvalidConfigurationAndUpdates(t *testing.T) {
	repository := &memoryRepository{}
	if _, err := Open(nil, testKey); err == nil {
		t.Fatal("nil repository was accepted")
	}
	for _, key := range []string{"", "not-base64", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := Open(repository, key); err == nil {
			t.Fatalf("invalid key %q was accepted", key)
		}
	}
	store, err := Open(repository, testKey)
	if err != nil {
		t.Fatal(err)
	}
	valid := "saved-openai"
	if err := store.Update(Update{OpenAIAPIKey: &valid}); err != nil {
		t.Fatal(err)
	}
	invalid := " pasted-key\n"
	if err := store.Update(Update{AnthropicAPIKey: &invalid}); err == nil {
		t.Fatal("invalid API key was accepted")
	}
	if got := environmentValue(store.Environment(), "OPENAI_API_KEY"); got != valid {
		t.Fatalf("saved OpenAI key = %q", got)
	}
	if err := store.Update(Update{}); err == nil {
		t.Fatal("empty update was accepted")
	}
	empty := ""
	if err := store.Update(Update{OpenAIAPIKey: &empty}); err == nil {
		t.Fatal("empty API key was accepted")
	}
	if _, err := Open(repository, base64.StdEncoding.EncodeToString([]byte("different-credential-key-1234567"))); err == nil {
		t.Fatal("mismatched key was accepted")
	}
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
