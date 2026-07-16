package taskcompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	markerFileName = "task-source-neutral.json"
	markerVersion  = 1
	boundaryName   = "source-neutral-task-v1"
)

var markerMu sync.Mutex

type Marker struct {
	Version   int       `json:"version"`
	Boundary  string    `json:"boundary"`
	CrossedAt time.Time `json:"crossedAt"`
}

func PathFor(dataFile string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(dataFile)), markerFileName)
}

func Ensure(dataFile string) error {
	markerMu.Lock()
	defer markerMu.Unlock()

	path := PathFor(dataFile)
	if _, err := Read(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("task compatibility marker: create directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".task-source-neutral-*")
	if err != nil {
		return fmt.Errorf("task compatibility marker: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("task compatibility marker: set permissions: %w", err)
	}
	marker := Marker{Version: markerVersion, Boundary: boundaryName, CrossedAt: time.Now().UTC()}
	if err := json.NewEncoder(temp).Encode(marker); err != nil {
		temp.Close()
		return fmt.Errorf("task compatibility marker: encode: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("task compatibility marker: sync: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("task compatibility marker: close: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("task compatibility marker: replace: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("task compatibility marker: open directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("task compatibility marker: sync directory: %w", err)
	}
	return nil
}

func Read(path string) (Marker, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Marker{}, err
	}
	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return Marker{}, fmt.Errorf("task compatibility marker: decode: %w", err)
	}
	if marker.Version != markerVersion || marker.Boundary != boundaryName || marker.CrossedAt.IsZero() {
		return Marker{}, errors.New("task compatibility marker: invalid marker")
	}
	return marker, nil
}
