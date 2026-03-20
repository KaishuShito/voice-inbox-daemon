package ingest

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"voice-inbox-daemon/internal/config"
	"voice-inbox-daemon/internal/state"
)

func TestCapturesRequiresBearerToken(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v0/captures", bytes.NewReader(nil))
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestCapturesRejectsMalformedMultipart(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v0/captures", bytes.NewBufferString("not-multipart"))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "multipart/form-data; boundary=broken")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCapturesPersistsFileAndRowBeforeAck(t *testing.T) {
	srv, st, audioRoot := newTestServer(t)
	req := newCaptureRequest(t, "cap-001", "pixel-8a", "2026-03-19T11:00:00Z", "audio/ogg", []byte("audio-bytes"))
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	capture, found, err := st.GetCapture("cap-001")
	if err != nil || !found {
		t.Fatalf("expected stored capture: found=%v err=%v", found, err)
	}
	if capture.Status != "pending" {
		t.Fatalf("expected pending status, got %s", capture.Status)
	}
	if capture.RawAudioPath == "" {
		t.Fatalf("expected raw audio path")
	}
	if _, err := os.Stat(capture.RawAudioPath); err != nil {
		t.Fatalf("expected persisted audio file: %v", err)
	}
	if !filepathHasPrefix(capture.RawAudioPath, audioRoot) {
		t.Fatalf("expected raw audio path inside audio root, got %s", capture.RawAudioPath)
	}
}

func TestCapturesAreIdempotentByCaptureID(t *testing.T) {
	srv, st, _ := newTestServer(t)

	first := httptest.NewRecorder()
	srv.Handler().ServeHTTP(first, newCaptureRequest(t, "cap-dup", "pixel-8a", "2026-03-19T11:00:00Z", "audio/ogg", []byte("one")))
	if first.Code != http.StatusCreated {
		t.Fatalf("expected first request 201, got %d", first.Code)
	}

	original, found, err := st.GetCapture("cap-dup")
	if err != nil || !found {
		t.Fatalf("expected original capture: found=%v err=%v", found, err)
	}

	second := httptest.NewRecorder()
	srv.Handler().ServeHTTP(second, newCaptureRequest(t, "cap-dup", "pixel-8a", "2026-03-19T11:00:00Z", "audio/ogg", []byte("two")))
	if second.Code != http.StatusOK {
		t.Fatalf("expected duplicate request 200, got %d", second.Code)
	}

	again, found, err := st.GetCapture("cap-dup")
	if err != nil || !found {
		t.Fatalf("expected duplicate capture record: found=%v err=%v", found, err)
	}
	if again.RawAudioPath != original.RawAudioPath {
		t.Fatalf("expected duplicate to keep original path, got %s vs %s", again.RawAudioPath, original.RawAudioPath)
	}
}

func TestCapturesDedupedBySourceKeyDoNotLeaveOrphanFile(t *testing.T) {
	srv, st, audioRoot := newTestServer(t)

	first := httptest.NewRecorder()
	srv.Handler().ServeHTTP(first, newCaptureRequestWithDedupeKey(t, "cap-a", "shared-key", "pixel-8a", "2026-03-19T11:00:00Z", "audio/ogg", []byte("one")))
	if first.Code != http.StatusCreated {
		t.Fatalf("expected first request 201, got %d", first.Code)
	}

	original, found, err := st.GetCapture("cap-a")
	if err != nil || !found {
		t.Fatalf("expected original capture: found=%v err=%v", found, err)
	}

	second := httptest.NewRecorder()
	srv.Handler().ServeHTTP(second, newCaptureRequestWithDedupeKey(t, "cap-b", "shared-key", "pixel-8a", "2026-03-19T11:01:00Z", "audio/ogg", []byte("two")))
	if second.Code != http.StatusOK {
		t.Fatalf("expected duplicate request 200, got %d", second.Code)
	}

	dup, found, err := st.GetCapture("cap-b")
	if err != nil {
		t.Fatalf("lookup duplicate capture: %v", err)
	}
	if found {
		t.Fatalf("expected no row for deduped capture, got %+v", dup)
	}

	files, err := collectFiles(audioRoot)
	if err != nil {
		t.Fatalf("collect files: %v", err)
	}
	if len(files) != 1 || files[0] != original.RawAudioPath {
		t.Fatalf("expected only original raw file to remain, got %v", files)
	}
}

func newTestServer(t *testing.T) (*Server, *state.Store, string) {
	t.Helper()
	tmp := t.TempDir()
	audioRoot := filepath.Join(tmp, "audio")
	st, err := state.Open(filepath.Join(tmp, "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Config{
		AudioStoreDir:    audioRoot,
		IngestAuthToken:  "test-token",
		IngestMaxBodyMB:  8,
		IngestSourceName: "android-voice-inbox",
	}
	if err := os.MkdirAll(audioRoot, 0o755); err != nil {
		t.Fatalf("mkdir audio root: %v", err)
	}
	return NewServer(cfg, st), st, audioRoot
}

func newCaptureRequest(t *testing.T, captureID, deviceID, capturedAt, contentType string, audio []byte) *http.Request {
	return newCaptureRequestWithDedupeKey(t, captureID, "", deviceID, capturedAt, contentType, audio)
}

func newCaptureRequestWithDedupeKey(t *testing.T, captureID, dedupeKey, deviceID, capturedAt, contentType string, audio []byte) *http.Request {
	t.Helper()
	_ = contentType
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("capture_id", captureID)
	if dedupeKey != "" {
		_ = writer.WriteField("source_dedupe_key", dedupeKey)
	}
	_ = writer.WriteField("device_id", deviceID)
	_ = writer.WriteField("captured_at", capturedAt)
	part, err := writer.CreateFormFile("audio", "memo.ogg")
	if err != nil {
		t.Fatalf("create audio part: %v", err)
	}
	if _, err := part.Write(audio); err != nil {
		t.Fatalf("write audio part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v0/captures", &body)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func collectFiles(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func filepathHasPrefix(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func TestCaptureResponseJSONShape(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, newCaptureRequest(t, "cap-json", "pixel-8a", time.Now().UTC().Format(time.RFC3339), "audio/ogg", []byte("audio")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	var payload captureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.CaptureID != "cap-json" || payload.Status != "pending" || payload.Source != "android-voice-inbox" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}
