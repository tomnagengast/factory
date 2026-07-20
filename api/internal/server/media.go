package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tomnagengast/factory/api/internal/state"
)

const maxMediaSize int64 = 25 << 20

var mediaTypes = map[string]string{
	".gif":  "image/gif",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".mp4":  "video/mp4",
	".mov":  "video/quicktime",
	".png":  "image/png",
	".webm": "video/webm",
	".webp": "image/webp",
}

type mediaResponse struct {
	state.Media
	URL string `json:"url"`
}

type mediaRequestError struct {
	status int
	err    error
}

func (e *mediaRequestError) Error() string { return e.err.Error() }

func (s *Server) mediaCreate(writer http.ResponseWriter, request *http.Request) {
	media, err := s.storeMedia(request)
	if err != nil {
		var requestError *mediaRequestError
		if errors.As(err, &requestError) {
			writeError(writer, requestError.status, requestError.err)
			return
		}
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusCreated, mediaResponse{
		Media: media, URL: fmt.Sprintf("/api/media/%d", media.ID),
	})
}

func (s *Server) storeMedia(request *http.Request) (state.Media, error) {
	reader, err := request.MultipartReader()
	if err != nil {
		return state.Media{}, badMediaRequest(http.StatusBadRequest, "request must be multipart form data")
	}
	part, err := reader.NextPart()
	if err != nil {
		return state.Media{}, badMediaRequest(http.StatusBadRequest, "multipart request requires one file field")
	}
	defer part.Close()
	if part.FormName() != "file" || part.FileName() == "" {
		return state.Media{}, badMediaRequest(http.StatusBadRequest, "multipart request requires one file field named file")
	}
	name := mediaFileName(part.FileName())
	if name == "" {
		return state.Media{}, badMediaRequest(http.StatusBadRequest, "media filename is required")
	}
	contentType, err := mediaContentType(part, name)
	if err != nil {
		return state.Media{}, err
	}

	temporary, err := os.CreateTemp(s.mediaRoot, ".upload-*")
	if err != nil {
		return state.Media{}, fmt.Errorf("create media temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(part, maxMediaSize+1))
	if err != nil {
		return state.Media{}, badMediaRequest(http.StatusBadRequest, "read uploaded media")
	}
	if size > maxMediaSize {
		return state.Media{}, badMediaRequest(http.StatusRequestEntityTooLarge, "media exceeds the 25 MiB limit")
	}
	if err := request.Context().Err(); err != nil {
		return state.Media{}, badMediaRequest(http.StatusBadRequest, "media upload was canceled")
	}
	if err := part.Close(); err != nil {
		return state.Media{}, badMediaRequest(http.StatusBadRequest, "finish uploaded media")
	}
	if extra, nextErr := reader.NextPart(); nextErr == nil {
		_ = extra.Close()
		return state.Media{}, badMediaRequest(http.StatusBadRequest, "multipart request accepts one file only")
	} else if !errors.Is(nextErr, io.EOF) {
		return state.Media{}, badMediaRequest(http.StatusBadRequest, "finish multipart request")
	}
	if err := temporary.Sync(); err != nil {
		return state.Media{}, fmt.Errorf("sync uploaded media: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return state.Media{}, fmt.Errorf("close uploaded media: %w", err)
	}

	sha := hex.EncodeToString(hash.Sum(nil))
	finalPath := filepath.Join(s.mediaRoot, sha)
	if err := os.Link(temporaryPath, finalPath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return state.Media{}, fmt.Errorf("finalize uploaded media: %w", err)
		}
		existing, verifyErr := verifiedMediaFile(finalPath, size, sha)
		if verifyErr != nil {
			return state.Media{}, errors.New("existing media blob is invalid")
		}
		_ = existing.Close()
	}

	event, err := s.store.Append(state.MediaCreated, state.MediaData{
		Name: name, ContentType: contentType, Size: size, SHA256: sha,
	})
	if err != nil {
		return state.Media{}, fmt.Errorf("publish media: %w", err)
	}
	return state.Media{
		Record: state.Record{ID: event.ID, CreatedAt: event.At, UpdatedAt: event.At},
		Name:   name, ContentType: contentType, Size: size, SHA256: sha,
	}, nil
}

func (s *Server) media(writer http.ResponseWriter, request *http.Request) {
	id, err := pathID(request, "media")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	mediaFile, found, err := s.store.Media(id)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if !found {
		http.NotFound(writer, request)
		return
	}
	file, disposition, err := s.openMedia(mediaFile)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, errors.New("media is unavailable"))
		return
	}
	defer file.Close()

	writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	writer.Header().Set("Content-Disposition", disposition)
	writer.Header().Set("Content-Type", mediaFile.ContentType)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(writer, request, mediaFile.Name, mediaFile.CreatedAt, file)
}

func (s *Server) openMedia(mediaFile state.Media) (*os.File, string, error) {
	if len(mediaFile.SHA256) != sha256.Size*2 || strings.ToLower(mediaFile.SHA256) != mediaFile.SHA256 {
		return nil, "", errors.New("invalid media hash")
	}
	if _, err := hex.DecodeString(mediaFile.SHA256); err != nil {
		return nil, "", errors.New("invalid media hash")
	}
	if !supportedMediaType(mediaFile.ContentType) || mediaFile.Size < 0 || mediaFile.Size > maxMediaSize {
		return nil, "", errors.New("invalid media metadata")
	}
	if mediaFile.Name == "" || strings.ContainsAny(mediaFile.Name, "\x00\r\n") {
		return nil, "", errors.New("invalid media name")
	}
	disposition := mime.FormatMediaType("inline", map[string]string{"filename": mediaFile.Name})
	if disposition == "" {
		return nil, "", errors.New("invalid media disposition")
	}

	candidate := filepath.Join(s.mediaRoot, mediaFile.SHA256)
	if filepath.Dir(candidate) != s.mediaRoot {
		return nil, "", errors.New("invalid media path")
	}
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", errors.New("invalid media blob")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || filepath.Dir(resolved) != s.mediaRoot || filepath.Base(resolved) != mediaFile.SHA256 {
		return nil, "", errors.New("invalid media blob path")
	}
	file, err := verifiedMediaFile(candidate, mediaFile.Size, mediaFile.SHA256)
	if err != nil {
		return nil, "", errors.New("media blob does not match metadata")
	}
	return file, disposition, nil
}

func verifiedMediaFile(path string, expectedSize int64, expectedSHA string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("media blob is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != expectedSize || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, errors.New("media blob size or identity does not match")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil || hex.EncodeToString(hash.Sum(nil)) != expectedSHA {
		_ = file.Close()
		return nil, errors.New("media blob hash does not match")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func mediaContentType(part *multipart.Part, name string) (string, error) {
	contentType := strings.TrimSpace(part.Header.Get("Content-Type"))
	if contentType != "" {
		parsed, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return "", badMediaRequest(http.StatusUnsupportedMediaType, "unsupported media type")
		}
		contentType = strings.ToLower(parsed)
	}
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = mediaTypes[strings.ToLower(filepath.Ext(name))]
	}
	if !supportedMediaType(contentType) {
		return "", badMediaRequest(http.StatusUnsupportedMediaType, "unsupported media type")
	}
	return contentType, nil
}

func supportedMediaType(contentType string) bool {
	for _, allowed := range mediaTypes {
		if contentType == allowed {
			return true
		}
	}
	return false
}

func mediaFileName(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	value = filepath.Base(value)
	if value == "." || value == "/" || strings.ContainsAny(value, "\x00\r\n") {
		return ""
	}
	return strings.TrimSpace(value)
}

func badMediaRequest(status int, message string) error {
	return &mediaRequestError{status: status, err: errors.New(message)}
}
