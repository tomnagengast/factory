package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func validateProjectRepository(repository string) error {
	parsed, err := url.ParseRequestURI(repository)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("project repository must be a public https://github.com/owner/repository URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || strings.TrimSuffix(parts[1], ".git") == "" {
		return errors.New("project repository must be a public https://github.com/owner/repository URL")
	}
	return nil
}

func cloneProjectRepository(ctx context.Context, repository, path string) error {
	return cloneProjectRepositoryTo(ctx, repository, path, false)
}

func cloneProjectRepositoryTo(ctx context.Context, repository, path string, allowEmpty bool) error {
	if err := validateProjectRepository(repository); err != nil {
		return err
	}
	replaceEmpty := false
	if info, err := os.Lstat(path); err == nil {
		if !allowEmpty {
			return errors.New("clone project repository: local path already exists")
		}
		if !info.IsDir() {
			return errors.New("sync project repository: local path is not a directory")
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return fmt.Errorf("read project path: %w", err)
		}
		if len(entries) != 0 {
			return errors.New("sync project repository: local path is not empty")
		}
		replaceEmpty = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect project path: %w", err)
	}

	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o777); err != nil {
		return fmt.Errorf("create project path parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".factory-clone-")
	if err != nil {
		return fmt.Errorf("create temporary clone path: %w", err)
	}
	defer os.RemoveAll(temporary) //nolint:errcheck -- best-effort cleanup after clone failure or rename

	if _, err := runGit(ctx, "clone", "--quiet", "--", repository, temporary); err != nil {
		return fmt.Errorf("clone project repository: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !replaceEmpty || !info.IsDir() {
			return errors.New("clone project repository: local path was created while cloning")
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return fmt.Errorf("read project path: %w", err)
		}
		if len(entries) != 0 {
			return errors.New("sync project repository: local path changed while cloning")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect project path: %w", err)
	}
	if replaceEmpty {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("replace empty project path: %w", err)
		}
	}
	if err := os.Rename(temporary, path); err != nil {
		if replaceEmpty {
			_ = os.Mkdir(path, 0o777)
		}
		return fmt.Errorf("move cloned repository into project path: %w", err)
	}
	return nil
}

func syncProjectRepository(ctx context.Context, repository, path string) error {
	return cloneProjectRepositoryTo(ctx, repository, path, true)
}

func projectRepositorySyncAvailable(repository, path string) bool {
	if validateProjectRepository(strings.TrimSpace(repository)) != nil {
		return false
	}
	info, err := os.Lstat(strings.TrimSpace(path))
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(strings.TrimSpace(path))
	return err == nil && len(entries) == 0
}

func runGit(ctx context.Context, arguments ...string) (string, error) {
	arguments = append([]string{"-c", "credential.helper="}, arguments...)
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	detail := strings.TrimSpace(string(output))
	if err != nil {
		if detail == "" {
			detail = err.Error()
		}
		return "", errors.New(detail)
	}
	return detail, nil
}
