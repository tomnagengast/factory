package store

import (
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) CredentialCiphertext() ([]byte, bool, error) {
	var ciphertext []byte
	err := s.db.QueryRow(`SELECT data FROM credentials WHERE id = 1`).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read credentials: %w", err)
	}
	return append([]byte(nil), ciphertext...), true, nil
}

func (s *Store) SaveCredentialCiphertext(ciphertext []byte) error {
	if len(ciphertext) == 0 {
		return errors.New("credential ciphertext is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO credentials(id, data) VALUES(1, ?)
		ON CONFLICT(id) DO UPDATE SET data = EXCLUDED.data
	`, ciphertext)
	if err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return nil
}
