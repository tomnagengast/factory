package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
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

	var content bytes.Buffer
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(&content, hash), io.LimitReader(part, maxMediaSize+1))
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
	sha := hex.EncodeToString(hash.Sum(nil))
	if err := s.objects.Put(request.Context(), "media/"+sha, content.Bytes(), contentType); err != nil {
		return state.Media{}, fmt.Errorf("store uploaded media: %w", err)
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
	content, disposition, err := s.openMedia(request.Context(), mediaFile)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, errors.New("media is unavailable"))
		return
	}
	writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	writer.Header().Set("Content-Disposition", disposition)
	writer.Header().Set("Content-Type", mediaFile.ContentType)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(writer, request, mediaFile.Name, mediaFile.CreatedAt, bytes.NewReader(content))
}

func (s *Server) openMedia(ctx context.Context, mediaFile state.Media) ([]byte, string, error) {
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

	content, err := s.objects.Get(ctx, "media/"+mediaFile.SHA256)
	if err != nil || int64(len(content)) != mediaFile.Size {
		return nil, "", errors.New("media blob does not match metadata")
	}
	hash := sha256.Sum256(content)
	if hex.EncodeToString(hash[:]) != mediaFile.SHA256 {
		return nil, "", errors.New("media blob does not match metadata")
	}
	return content, disposition, nil
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
