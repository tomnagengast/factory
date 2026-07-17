package activation

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const leaseContractVersion = 1

var ErrLeaseUnavailable = errors.New("activation: state-transition lease is unavailable")

type leaseRecord struct {
	ContractVersion int    `json:"contractVersion"`
	OwnerPID        int    `json:"ownerPid"`
	OwnerStartTime  string `json:"ownerStartTime"`
	TokenSHA256     string `json:"tokenSha256"`
}

// Lease is the continuously held state-transition authority accepted by the
// provider guard. The token never enters the on-disk record or logs.
type Lease struct {
	path  string
	file  *os.File
	token string
}

func AcquireLease(path string) (*Lease, error) {
	path = filepath.Clean(path)
	if path == "." || path == string(os.PathSeparator) {
		return nil, errors.New("activation: state-transition lease path is required")
	}
	if err := ensurePrivateParent(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := openLeaseFile(path)
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = file.Close()
		}
	}()
	if err := validateLeaseFile(path, file); err != nil {
		return nil, err
	}
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart, Start: 0, Len: 0}
	if err := syscall.FcntlFlock(file.Fd(), syscall.F_SETLK, &lock); err != nil {
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLeaseUnavailable
		}
		return nil, fmt.Errorf("activation: lock state-transition lease: %w", err)
	}
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tokenBytes); err != nil {
		return nil, fmt.Errorf("activation: create state-transition token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	digest := sha256.Sum256([]byte(token))
	startTime, err := processStartTime(os.Getpid())
	if err != nil {
		return nil, err
	}
	record := leaseRecord{
		ContractVersion: leaseContractVersion, OwnerPID: os.Getpid(),
		OwnerStartTime: startTime, TokenSHA256: hex.EncodeToString(digest[:]),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := file.Truncate(0); err != nil {
		return nil, fmt.Errorf("activation: truncate state-transition lease: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("activation: seek state-transition lease: %w", err)
	}
	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return nil, fmt.Errorf("activation: write state-transition lease: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("activation: sync state-transition lease: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := validateLeaseFile(path, file); err != nil {
		return nil, err
	}
	succeeded = true
	return &Lease{path: path, file: file, token: token}, nil
}

func (l *Lease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Environment returns the private capability values needed only by a guarded
// provider child. The descriptor remains valid until Close.
func (l *Lease) Environment() []string {
	if l == nil || l.file == nil {
		return nil
	}
	return []string{
		"NAGS_FACTORY_STATE_LEASE_FD=" + strconv.FormatUint(uint64(l.file.Fd()), 10),
		"NAGS_FACTORY_STATE_LEASE_TOKEN=" + l.token,
	}
}

func (l *Lease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	lock := syscall.Flock_t{Type: syscall.F_UNLCK, Whence: io.SeekStart, Start: 0, Len: 0}
	unlockErr := syscall.FcntlFlock(l.file.Fd(), syscall.F_SETLK, &lock)
	closeErr := l.file.Close()
	l.file, l.token = nil, ""
	return errors.Join(unlockErr, closeErr)
}

func openLeaseFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("activation: state-transition lease must not be a symlink")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	flags := os.O_RDWR
	if errors.Is(err, os.ErrNotExist) {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("activation: open state-transition lease: %w", err)
	}
	return file, nil
}

func validateLeaseFile(path string, file *os.File) error {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return err
	}
	pathStat, pathOK := pathInfo.Sys().(*syscall.Stat_t)
	fileStat, fileOK := fileInfo.Sys().(*syscall.Stat_t)
	if !pathOK || !fileOK {
		return errors.New("activation: state-transition lease identity is unavailable")
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm() != 0o600 ||
		pathStat.Dev != fileStat.Dev || pathStat.Ino != fileStat.Ino || fileStat.Nlink != 1 || fileStat.Uid != uint32(os.Getuid()) {
		return errors.New("activation: state-transition lease file is unsafe")
	}
	return nil
}

func ensurePrivateParent(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("activation: state-transition directory is unsafe")
	}
	return nil
}

func processStartTime(pid int) (string, error) {
	command := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	value := strings.Join(strings.Fields(string(output)), " ")
	if err != nil || value == "" {
		return "", fmt.Errorf("activation: inspect process start time: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return value, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	return errors.Join(err, closeErr)
}
