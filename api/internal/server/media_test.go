package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/state"
)

func TestMediaUploadRetrievalAndRanges(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	root := t.TempDir()
	handler := testServerWithMedia(t, wire, root).Handler()
	tests := []struct {
		name, contentType string
		data              []byte
	}{
		{"sample.png", "image/png", []byte("png-bytes")},
		{"sample.jpg", "image/jpeg", []byte("jpeg-bytes")},
		{"sample.gif", "image/gif", []byte("gif89a-bytes")},
		{"sample.webp", "image/webp", []byte("webp-bytes")},
		{"sample.mp4", "video/mp4", []byte("mp4-video")},
		{"sample.webm", "video/webm", []byte("webm-video")},
		{"sample.mov", "video/quicktime", []byte("quicktime-video")},
	}
	for _, test := range tests {
		t.Run(test.contentType, func(t *testing.T) {
			response := uploadRequest(t, handler, test.name, test.contentType, test.data)
			if response.Code != http.StatusCreated {
				t.Fatalf("upload = %d %s", response.Code, response.Body)
			}
			var created mediaResponse
			if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			if created.ID < 1 || created.Name != test.name || created.ContentType != test.contentType ||
				created.Size != int64(len(test.data)) || created.URL != fmt.Sprintf("/api/media/%d", created.ID) {
				t.Fatalf("created = %#v", created)
			}
			get := requestJSON(t, handler, http.MethodGet, created.URL, "")
			if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), test.data) {
				t.Fatalf("get = %d %q", get.Code, get.Body.Bytes())
			}
			if get.Header().Get("Content-Type") != test.contentType ||
				get.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
				get.Header().Get("X-Content-Type-Options") != "nosniff" ||
				!strings.HasPrefix(get.Header().Get("Content-Disposition"), "inline") {
				t.Fatalf("headers = %#v", get.Header())
			}
			rangeRequest := httptest.NewRequest(http.MethodGet, created.URL, nil)
			rangeRequest.Header.Set("Range", "bytes=1-3")
			rangeResponse := httptest.NewRecorder()
			handler.ServeHTTP(rangeResponse, rangeRequest)
			if rangeResponse.Code != http.StatusPartialContent ||
				!bytes.Equal(rangeResponse.Body.Bytes(), test.data[1:4]) {
				t.Fatalf("range = %d %q", rangeResponse.Code, rangeResponse.Body.Bytes())
			}
			event, found := wire.Event(created.ID)
			if !found || event.Type != state.MediaCreated || bytes.Contains(event.Data, test.data) {
				t.Fatalf("wire event = %#v", event)
			}
		})
	}
	assertNoMediaTemps(t, root)
}

func TestMediaUploadInfersGenericContentTypeFromExtension(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	response := uploadRequest(t, testServerWithMedia(t, wire, t.TempDir()).Handler(),
		"generic.webp", "application/octet-stream", []byte("webp"))
	var created mediaResponse
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &created) != nil ||
		created.ContentType != "image/webp" {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
}

func TestMediaUploadRejectsBadRequestsWithoutResidue(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		wire := openWire(t)
		defer wire.Close()
		root := t.TempDir()
		response := uploadRequest(t, testServerWithMedia(t, wire, root).Handler(), "notes.txt", "text/plain", []byte("no"))
		if response.Code != http.StatusUnsupportedMediaType || wire.LastID() != 0 {
			t.Fatalf("response = %d %s, events = %d", response.Code, response.Body, wire.LastID())
		}
		assertNoMediaTemps(t, root)
	})

	t.Run("oversize", func(t *testing.T) {
		wire := openWire(t)
		defer wire.Close()
		root := t.TempDir()
		response := uploadRequest(t, testServerWithMedia(t, wire, root).Handler(),
			"large.mp4", "video/mp4", bytes.Repeat([]byte{'x'}, int(maxMediaSize+1)))
		if response.Code != http.StatusRequestEntityTooLarge || wire.LastID() != 0 {
			t.Fatalf("response = %d %s, events = %d", response.Code, response.Body, wire.LastID())
		}
		assertNoMediaTemps(t, root)
	})

	t.Run("malformed", func(t *testing.T) {
		wire := openWire(t)
		defer wire.Close()
		root := t.TempDir()
		handler := testServerWithMedia(t, wire, root).Handler()
		request := httptest.NewRequest(http.MethodPost, "/api/media", strings.NewReader("not multipart"))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || wire.LastID() != 0 {
			t.Fatalf("response = %d %s", response.Code, response.Body)
		}
		assertNoMediaTemps(t, root)
	})

	t.Run("canceled", func(t *testing.T) {
		wire := openWire(t)
		defer wire.Close()
		root := t.TempDir()
		request := newUploadRequest(t, "canceled.png", "image/png", []byte("cancel me"))
		ctx, cancel := context.WithCancel(request.Context())
		cancel()
		request = request.WithContext(ctx)
		response := httptest.NewRecorder()
		testServerWithMedia(t, wire, root).Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || wire.LastID() != 0 {
			t.Fatalf("response = %d %s", response.Code, response.Body)
		}
		assertNoMediaTemps(t, root)
	})

	t.Run("interrupted copy", func(t *testing.T) {
		wire := openWire(t)
		defer wire.Close()
		root := t.TempDir()
		request := newUploadRequest(t, "interrupted.png", "image/png", []byte("copy will fail"))
		encoded, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		fileStart := bytes.Index(encoded, []byte("copy will fail"))
		if fileStart < 0 {
			t.Fatal("multipart file body not found")
		}
		request.Body = io.NopCloser(&interruptedReader{data: encoded, limit: fileStart + 3})
		response := httptest.NewRecorder()
		testServerWithMedia(t, wire, root).Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || wire.LastID() != 0 {
			t.Fatalf("response = %d %s", response.Code, response.Body)
		}
		assertNoMediaTemps(t, root)
	})

	t.Run("finalization failure", func(t *testing.T) {
		wire := openWire(t)
		defer wire.Close()
		root := t.TempDir()
		data := []byte("blocked")
		hash := mediaHash(data)
		if err := os.Mkdir(filepath.Join(root, hash), 0o700); err != nil {
			t.Fatal(err)
		}
		response := uploadRequest(t, testServerWithMedia(t, wire, root).Handler(), "blocked.png", "image/png", data)
		if response.Code != http.StatusInternalServerError || wire.LastID() != 0 {
			t.Fatalf("response = %d %s", response.Code, response.Body)
		}
		assertNoMediaTemps(t, root)
	})
}

func TestMediaPublicationFailureRetainsFinalBlob(t *testing.T) {
	wire := openWire(t)
	root := t.TempDir()
	handler := testServerWithMedia(t, wire, root).Handler()
	if err := wire.Close(); err != nil {
		t.Fatal(err)
	}
	data := []byte("retained")
	response := uploadRequest(t, handler, "retained.png", "image/png", data)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
	stored, err := os.ReadFile(filepath.Join(root, mediaHash(data)))
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("retained blob = %q, %v", stored, err)
	}
	assertNoMediaTemps(t, root)
}

func TestConcurrentIdenticalMediaUploadsShareBlob(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	root := t.TempDir()
	handler := testServerWithMedia(t, wire, root).Handler()
	data := []byte("same immutable bytes")
	const count = 12
	responses := make([]*httptest.ResponseRecorder, count)
	var wait sync.WaitGroup
	for index := range responses {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses[index] = uploadRequest(t, handler, "same.gif", "image/gif", data)
		}()
	}
	wait.Wait()
	for _, response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("upload = %d %s", response.Code, response.Body)
		}
		var created mediaResponse
		if json.Unmarshal(response.Body.Bytes(), &created) != nil {
			t.Fatalf("created = %s", response.Body)
		}
		get := requestJSON(t, handler, http.MethodGet, created.URL, "")
		if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), data) {
			t.Fatalf("get = %d %q", get.Code, get.Body.Bytes())
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != mediaHash(data) {
		t.Fatalf("media entries = %#v", entries)
	}
}

func TestMediaRetrievalRejectsUntrustedProjectedMetadata(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	root := t.TempDir()
	handler := testServerWithMedia(t, wire, root).Handler()
	outside := filepath.Join(t.TempDir(), "outside-secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte("safe")
	validHash := mediaHash(data)
	if err := os.WriteFile(filepath.Join(root, validHash), data, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []state.MediaData{
		{Name: "escape.png", ContentType: "image/png", Size: 6, SHA256: "../outside-secret"},
		{Name: "upper.png", ContentType: "image/png", Size: 6, SHA256: strings.ToUpper(validHash)},
		{Name: "mime.png", ContentType: "image/png\r\nX-Evil: yes", Size: 4, SHA256: validHash},
		{Name: "name\r\nX-Evil: yes.png", ContentType: "image/png", Size: 4, SHA256: validHash},
		{Name: "size.png", ContentType: "image/png", Size: maxMediaSize + 1, SHA256: validHash},
	}
	for _, metadata := range tests {
		createdID := publishMediaEvent(t, handler, metadata)
		response := requestJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/media/%d", createdID), "")
		if response.Code != http.StatusInternalServerError ||
			response.Header().Get("Content-Disposition") != "" ||
			strings.Contains(response.Header().Get("Content-Type"), "X-Evil") ||
			strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("metadata %#v response = %d %#v %s", metadata, response.Code, response.Header(), response.Body)
		}
	}

	symlinkHash := strings.Repeat("a", 64)
	if err := os.Symlink(outside, filepath.Join(root, symlinkHash)); err != nil {
		t.Fatal(err)
	}
	id := publishMediaEvent(t, handler, state.MediaData{
		Name: "outside.png", ContentType: "image/png", Size: 6, SHA256: symlinkHash,
	})
	if response := requestJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/media/%d", id), ""); response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("symlink response = %d %s", response.Code, response.Body)
	}
}

func TestMediaPersistsAcrossWireAndServerRestart(t *testing.T) {
	base := t.TempDir()
	wirePath := filepath.Join(base, "wire.jsonl")
	mediaRoot := filepath.Join(base, "media")
	wire, err := eventwire.Open(wirePath)
	if err != nil {
		t.Fatal(err)
	}
	handler := testServerWithMedia(t, wire, mediaRoot).Handler()
	data := []byte("restart-video")
	upload := uploadRequest(t, handler, "restart.webm", "video/webm", data)
	var created mediaResponse
	if upload.Code != http.StatusCreated || json.Unmarshal(upload.Body.Bytes(), &created) != nil {
		t.Fatalf("upload = %d %s", upload.Code, upload.Body)
	}
	if err := wire.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := eventwire.Open(wirePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	handler = testServerWithMedia(t, reopened, mediaRoot).Handler()
	get := requestJSON(t, handler, http.MethodGet, created.URL, "")
	if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), data) {
		t.Fatalf("restarted get = %d %q", get.Code, get.Body.Bytes())
	}
}

func TestMediaReferencesPersistInTasksAndThreadedComments(t *testing.T) {
	base := t.TempDir()
	wirePath := filepath.Join(base, "wire.jsonl")
	mediaRoot := filepath.Join(base, "media")
	wire, err := eventwire.Open(wirePath)
	if err != nil {
		t.Fatal(err)
	}
	handler := testServerWithMedia(t, wire, mediaRoot).Handler()
	markups := make([]string, 0, 3)
	for _, fixture := range []struct{ name, kind string }{
		{"screen.png", "image/png"}, {"motion.gif", "image/gif"}, {"clip.mp4", "video/mp4"},
	} {
		upload := uploadRequest(t, handler, fixture.name, fixture.kind, []byte(fixture.name))
		var created mediaResponse
		if upload.Code != http.StatusCreated || json.Unmarshal(upload.Body.Bytes(), &created) != nil {
			t.Fatalf("upload = %d %s", upload.Code, upload.Body)
		}
		markups = append(markups, created.URL)
	}
	projectPath := filepath.Join(base, "project")
	project := requestJSON(t, handler, http.MethodPost, "/api/projects",
		fmt.Sprintf(`{"name":"Media","path":%q}`, projectPath))
	var projectRecord state.Project
	if project.Code != http.StatusCreated || json.Unmarshal(project.Body.Bytes(), &projectRecord) != nil {
		t.Fatalf("project = %d %s", project.Code, project.Body)
	}
	description := strings.Join(markups, "\n")
	taskBody, _ := json.Marshal(map[string]any{
		"title": "Media task", "description": description, "status": "todo", "projectId": projectRecord.ID,
	})
	task := requestJSON(t, handler, http.MethodPost, "/api/tasks", string(taskBody))
	var taskRecord state.Task
	if task.Code != http.StatusCreated || json.Unmarshal(task.Body.Bytes(), &taskRecord) != nil {
		t.Fatalf("task = %d %s", task.Code, task.Body)
	}
	updatedDescription := "updated\n" + description
	updateBody, _ := json.Marshal(map[string]any{
		"title": "Media task", "description": updatedDescription, "status": "in progress", "projectId": projectRecord.ID,
	})
	if response := requestJSON(t, handler, http.MethodPut, fmt.Sprintf("/api/tasks/%d", taskRecord.ID), string(updateBody)); response.Code != http.StatusOK {
		t.Fatalf("update = %d %s", response.Code, response.Body)
	}
	rootBody, _ := json.Marshal(map[string]string{"content": markups[0]})
	root := requestJSON(t, handler, http.MethodPost, fmt.Sprintf("/api/tasks/%d/comments", taskRecord.ID), string(rootBody))
	var rootComment state.Comment
	if root.Code != http.StatusCreated || json.Unmarshal(root.Body.Bytes(), &rootComment) != nil {
		t.Fatalf("root comment = %d %s", root.Code, root.Body)
	}
	replyBody, _ := json.Marshal(map[string]any{"content": markups[2], "parentCommentId": rootComment.ID})
	if reply := requestJSON(t, handler, http.MethodPost, fmt.Sprintf("/api/tasks/%d/comments", taskRecord.ID), string(replyBody)); reply.Code != http.StatusCreated {
		t.Fatalf("reply = %d %s", reply.Code, reply.Body)
	}
	if err := wire.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := eventwire.Open(wirePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	handler = testServerWithMedia(t, reopened, mediaRoot).Handler()
	detail := requestJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/tasks/%d", taskRecord.ID), "")
	var result struct {
		Task     state.Task      `json:"task"`
		Comments []state.Comment `json:"comments"`
	}
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &result) != nil ||
		result.Task.Description == nil || *result.Task.Description != updatedDescription || len(result.Comments) != 2 ||
		result.Comments[0].Content != markups[0] || result.Comments[1].Content != markups[2] {
		t.Fatalf("restarted detail = %d %#v", detail.Code, result)
	}
}

func TestMediaUnknownAndMissingBlob(t *testing.T) {
	wire := openWire(t)
	defer wire.Close()
	root := t.TempDir()
	handler := testServerWithMedia(t, wire, root).Handler()
	if response := requestJSON(t, handler, http.MethodGet, "/api/media/99", ""); response.Code != http.StatusNotFound {
		t.Fatalf("unknown = %d %s", response.Code, response.Body)
	}
	upload := uploadRequest(t, handler, "gone.png", "image/png", []byte("gone"))
	var created mediaResponse
	if upload.Code != http.StatusCreated || json.Unmarshal(upload.Body.Bytes(), &created) != nil {
		t.Fatalf("upload = %d %s", upload.Code, upload.Body)
	}
	if err := os.Remove(filepath.Join(root, created.SHA256)); err != nil {
		t.Fatal(err)
	}
	if response := requestJSON(t, handler, http.MethodGet, created.URL, ""); response.Code != http.StatusInternalServerError {
		t.Fatalf("missing = %d %s", response.Code, response.Body)
	}

	corrupt := uploadRequest(t, handler, "corrupt.png", "image/png", []byte("good"))
	if corrupt.Code != http.StatusCreated || json.Unmarshal(corrupt.Body.Bytes(), &created) != nil {
		t.Fatalf("corrupt upload = %d %s", corrupt.Code, corrupt.Body)
	}
	if err := os.WriteFile(filepath.Join(root, created.SHA256), []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	if response := requestJSON(t, handler, http.MethodGet, created.URL, ""); response.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt = %d %s", response.Code, response.Body)
	}
}

func uploadRequest(t *testing.T, handler http.Handler, name, contentType string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newUploadRequest(t, name, contentType, data))
	return response
}

func newUploadRequest(t *testing.T, name, contentType string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name": "file", "filename": name,
	}))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/media", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func publishMediaEvent(t *testing.T, handler http.Handler, metadata state.MediaData) int64 {
	t.Helper()
	body, err := json.Marshal(map[string]any{"type": state.MediaCreated, "data": metadata})
	if err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, handler, http.MethodPost, "/api/events", string(body))
	if response.Code != http.StatusCreated {
		t.Fatalf("event = %d %s", response.Code, response.Body)
	}
	var event eventwire.Event
	if err := json.Unmarshal(response.Body.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	return event.ID
}

func mediaHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func assertNoMediaTemps(t *testing.T, root string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".upload-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary media files remain: %v", matches)
	}
}

func testServerWithMedia(t *testing.T, wire *eventwire.Wire, root string) *Server {
	t.Helper()
	assets := fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<html></html>")},
		"assets/app-a1.js":     &fstest.MapFile{Data: []byte("export {};")},
		"assets/styles-b2.css": &fstest.MapFile{Data: []byte("body {}")},
	}
	var filesystem fs.FS = assets
	server, err := New(wire, filesystem, root)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

type interruptedReader struct {
	data   []byte
	limit  int
	offset int
}

func (reader *interruptedReader) Read(target []byte) (int, error) {
	if reader.offset >= reader.limit {
		return 0, errors.New("interrupted upload")
	}
	remaining := reader.limit - reader.offset
	if len(target) > remaining {
		target = target[:remaining]
	}
	count := copy(target, reader.data[reader.offset:reader.limit])
	reader.offset += count
	return count, nil
}
