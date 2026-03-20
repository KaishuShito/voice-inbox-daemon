package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"voice-inbox-daemon/internal/config"
	"voice-inbox-daemon/internal/state"
)

type Server struct {
	cfg   config.Config
	store *state.Store
}

type captureResponse struct {
	CaptureID string `json:"capture_id"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	Duplicate bool   `json:"duplicate"`
	Received  string `json:"received_at"`
}

func NewServer(cfg config.Config, store *state.Store) *Server {
	return &Server{cfg: cfg, store: store}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v0/captures", s.handleCapture)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, int64(s.cfg.IngestMaxBodyMB)*1024*1024)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, fmt.Sprintf("invalid multipart body: %v", err), http.StatusBadRequest)
		return
	}

	captureID := strings.TrimSpace(r.FormValue("capture_id"))
	if captureID == "" {
		http.Error(w, "capture_id is required", http.StatusBadRequest)
		return
	}
	dedupeKey := strings.TrimSpace(r.FormValue("source_dedupe_key"))
	if dedupeKey == "" {
		dedupeKey = captureID
	}
	deviceID := strings.TrimSpace(r.FormValue("device_id"))
	var capturedAt *time.Time
	if raw := strings.TrimSpace(r.FormValue("captured_at")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "captured_at must be RFC3339", http.StatusBadRequest)
			return
		}
		capturedAt = &parsed
	}

	if rec, found, err := s.findDuplicate(captureID, dedupeKey); err != nil {
		http.Error(w, fmt.Sprintf("duplicate lookup failed: %v", err), http.StatusInternalServerError)
		return
	} else if found {
		writeJSON(w, http.StatusOK, captureResponse{
			CaptureID: rec.CaptureID,
			Status:    rec.Status,
			Source:    rec.Source,
			Duplicate: true,
			Received:  rec.ReceivedAt.UTC().Format(time.RFC3339),
		})
		return
	}

	file, header, err := r.FormFile("audio")
	if err != nil {
		http.Error(w, "audio file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	receivedAt := time.Now().UTC()
	rawPath, contentType, err := s.persistUpload(file, header.Filename, header.Header.Get("Content-Type"), captureID, receivedAt)
	if err != nil {
		http.Error(w, fmt.Sprintf("persist upload: %v", err), http.StatusInternalServerError)
		return
	}

	rec := state.CaptureRecord{
		CaptureID:       captureID,
		Source:          s.cfg.IngestSourceName,
		SourceDedupeKey: dedupeKey,
		DeviceID:        deviceID,
		CapturedAt:      capturedAt,
		ReceivedAt:      receivedAt,
		RawAudioPath:    rawPath,
		ContentType:     contentType,
		Status:          "pending",
	}
	if err := s.store.CreateCapture(rec); err != nil {
		if existing, found, lookupErr := s.findDuplicate(captureID, dedupeKey); lookupErr == nil && found {
			writeJSON(w, http.StatusOK, captureResponse{
				CaptureID: existing.CaptureID,
				Status:    existing.Status,
				Source:    existing.Source,
				Duplicate: true,
				Received:  existing.ReceivedAt.UTC().Format(time.RFC3339),
			})
			return
		}
		http.Error(w, fmt.Sprintf("create capture: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, captureResponse{
		CaptureID: captureID,
		Status:    "pending",
		Source:    s.cfg.IngestSourceName,
		Duplicate: false,
		Received:  receivedAt.Format(time.RFC3339),
	})
}

func (s *Server) authorized(r *http.Request) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return false
	}
	return header == "Bearer "+s.cfg.IngestAuthToken
}

func (s *Server) findDuplicate(captureID, dedupeKey string) (state.CaptureRecord, bool, error) {
	if captureID != "" {
		rec, found, err := s.store.GetCapture(captureID)
		if err != nil {
			return state.CaptureRecord{}, false, err
		}
		if found {
			return rec, true, nil
		}
	}
	if dedupeKey != "" {
		rec, found, err := s.store.GetCaptureBySourceDedupeKey(dedupeKey)
		if err != nil {
			return state.CaptureRecord{}, false, err
		}
		if found {
			return rec, true, nil
		}
	}
	return state.CaptureRecord{}, false, nil
}

func (s *Server) persistUpload(src io.Reader, filename, headerContentType, captureID string, receivedAt time.Time) (string, string, error) {
	subdir := filepath.Join(s.cfg.AudioStoreDir, "ingest", receivedAt.Format("2006/01/02"))
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		return "", "", err
	}

	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(filename)))
	contentType := strings.TrimSpace(headerContentType)
	if contentType == "" && ext != "" {
		contentType = mime.TypeByExtension(ext)
	}
	if ext == "" && contentType != "" {
		if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
			ext = exts[0]
		}
	}
	if ext == "" {
		ext = ".bin"
	}

	finalPath := filepath.Join(subdir, captureID+ext)
	tmp, err := os.CreateTemp(subdir, captureID+".*.tmp")
	if err != nil {
		return "", "", err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if _, statErr := os.Stat(tmpPath); statErr == nil {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, src); err != nil {
		return "", "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return "", "", err
	}
	if contentType == "" {
		contentType = mime.TypeByExtension(ext)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return finalPath, contentType, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
