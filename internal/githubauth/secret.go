package githubauth

import (
	"fmt"
	"os"
	"strings"
)

// ReadSecret reads a secret from exactly one of a file or an environment
// value. File-backed secrets keep credentials out of process environments on
// platforms that support mounted secrets.
func ReadSecret(fileVariable, valueVariable string) ([]byte, error) {
	file := strings.TrimSpace(os.Getenv(fileVariable))
	value, valueSet := os.LookupEnv(valueVariable)
	valueSet = valueSet && strings.TrimSpace(value) != ""
	if file != "" && valueSet {
		return nil, fmt.Errorf("set only one of %s and %s", fileVariable, valueVariable)
	}

	var secret []byte
	var err error
	switch {
	case file != "":
		secret, err = os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", fileVariable, err)
		}
	case valueSet:
		secret = []byte(value)
	default:
		return nil, fmt.Errorf("set %s or %s", fileVariable, valueVariable)
	}

	secret = []byte(strings.TrimSpace(string(secret)))
	if len(secret) < 32 {
		return nil, fmt.Errorf("secret from %s or %s must be at least 32 bytes", fileVariable, valueVariable)
	}
	return secret, nil
}
