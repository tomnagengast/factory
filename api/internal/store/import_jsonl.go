package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/tomnagengast/factory/api/internal/eventwire"
)

// ImportJSONL imports one legacy wire into an empty database. The entire import
// is one transaction, so a malformed line or projection failure leaves no
// partially imported database.
func (s *Store) ImportJSONL(ctx context.Context, path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open legacy event wire: %w", err)
	}
	defer file.Close()

	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	lastID, err := lastIDTx(tx)
	if err != nil {
		return 0, err
	}
	if lastID != 0 {
		return 0, errors.New("legacy wire import requires an empty database")
	}

	reader := bufio.NewReader(file)
	var imported int64
	var lineNumber int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			lineNumber++
			line = bytes.TrimSpace(line)
			var event eventwire.Event
			if err := json.Unmarshal(line, &event); err != nil {
				return 0, fmt.Errorf("decode legacy event wire line %d: %w", lineNumber, err)
			}
			if event.ID != imported+1 {
				return 0, fmt.Errorf("legacy event wire line %d has non-contiguous ID %d", lineNumber, event.ID)
			}
			if err := insertEvent(tx, event); err != nil {
				return 0, fmt.Errorf("import legacy event %d: %w", event.ID, err)
			}
			if err := applyProjection(tx, event); err != nil {
				return 0, fmt.Errorf("project legacy event %d (%s): %w", event.ID, event.Type, err)
			}
			imported = event.ID
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, fmt.Errorf("read legacy event wire: %w", readErr)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit legacy wire import: %w", err)
	}
	if imported > 0 {
		close(s.changed)
		s.changed = make(chan struct{})
	}
	return imported, nil
}
