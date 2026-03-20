package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"voice-inbox-daemon/internal/config"
	"voice-inbox-daemon/internal/discord"
	"voice-inbox-daemon/internal/journal"
	"voice-inbox-daemon/internal/obsidian"
	"voice-inbox-daemon/internal/state"
)

type discordMock struct {
	t            *testing.T
	server       *httptest.Server
	messages     []discord.Message
	reactionFail bool
	reactionHits int
	mu           sync.Mutex
}

func newDiscordMock(t *testing.T) *discordMock {
	t.Helper()
	dm := &discordMock{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v10/users/@me", dm.handleMe)
	mux.HandleFunc("/api/v10/channels/1476388224124325909/messages", dm.handleMessages)
	mux.HandleFunc("/api/v10/channels/1476388224124325909/messages/", dm.handleReaction)
	mux.HandleFunc("/attachments/", dm.handleAttachment)
	dm.server = httptest.NewServer(mux)
	return dm
}

func (d *discordMock) close() {
	d.server.Close()
}

func (d *discordMock) baseURL() string {
	return d.server.URL + "/api/v10"
}

func (d *discordMock) handleMe(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bot ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "bot", "username": "voice-bot"})
}

func (d *discordMock) handleMessages(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bot ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_ = json.NewEncoder(w).Encode(d.messages)
}

func (d *discordMock) handleReaction(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bot ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	d.reactionHits++
	fail := d.reactionFail
	d.mu.Unlock()
	if fail {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("reaction failed"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *discordMock) handleAttachment(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bot ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_, _ = w.Write([]byte("FAKE_AUDIO"))
}

type obsidianMock struct {
	t          *testing.T
	server     *httptest.Server
	files      map[string]string
	appendFail bool
	appendHits int
	mu         sync.Mutex
}

func newObsidianMock(t *testing.T) *obsidianMock {
	t.Helper()
	om := &obsidianMock{t: t, files: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", om.handleRoot)
	mux.HandleFunc("/vault/", om.handleVault)
	om.server = httptest.NewTLSServer(mux)
	return om
}

func (o *obsidianMock) close() {
	o.server.Close()
}

func (o *obsidianMock) handleRoot(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": true})
}

func (o *obsidianMock) handleVault(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	vaultPath := strings.TrimPrefix(r.URL.Path, "/vault/")
	decoded, err := decodeVaultPath(vaultPath)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	body, _ := ioReadAll(r)

	o.mu.Lock()
	defer o.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		content, ok := o.files[decoded]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(content))
	case http.MethodPut:
		o.files[decoded] = string(body)
		w.WriteHeader(http.StatusCreated)
	case http.MethodPost:
		o.appendHits++
		if o.appendFail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("append failed"))
			return
		}
		o.files[decoded] += string(body)
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func decodeVaultPath(raw string) (string, error) {
	parts := strings.Split(strings.TrimPrefix(raw, "/"), "/")
	decoded := make([]string, 0, len(parts))
	for _, p := range parts {
		u, err := url.PathUnescape(p)
		if err != nil {
			return "", err
		}
		decoded = append(decoded, u)
	}
	return strings.Join(decoded, "/"), nil
}

func ioReadAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func writeFakeBinary(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script %s: %v", path, err)
	}
}

func setupRunner(t *testing.T, dm *discordMock, om *obsidianMock) (*Runner, *state.Store, config.Config, func()) {
	t.Helper()
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ffmpegPath := filepath.Join(binDir, "ffmpeg")
	writeFakeBinary(t, ffmpegPath, `#!/bin/sh
set -eu
input=""
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-i" ]; then
    input="$arg"
  fi
  prev="$arg"
  out="$arg"
done
cp "$input" "$out"
`)

	whisperPath := filepath.Join(binDir, "whisper")
	writeFakeBinary(t, whisperPath, `#!/bin/sh
set -eu
in="$1"
out_dir="."
shift
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output_dir)
      shift
      out_dir="$1"
      ;;
  esac
  shift
done
base="$(basename "$in")"
base="${base%.*}"
mkdir -p "$out_dir"
printf '{"text":"テスト文字起こし"}' > "$out_dir/$base.json"
`)

	dbPath := filepath.Join(tmp, "state.db")
	st, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}

	cfg := config.Config{
		DiscordBotToken:         "test-token",
		DiscordAPIBaseURL:       dm.baseURL(),
		VoiceInboxChannelID:     "1476388224124325909",
		AllowedAuthorIDs:        map[string]struct{}{"968754117885456425": {}},
		DiscordFetchLimit:       100,
		PollIntervalSeconds:     300,
		WhisperBin:              whisperPath,
		WhisperModel:            "large-v3-turbo",
		WhisperLanguage:         "ja",
		FFmpegBin:               ffmpegPath,
		ObsidianBaseURL:         om.server.URL,
		ObsidianAPIKey:          "obsidian-key",
		ObsidianAuthHeader:      "Authorization",
		ObsidianVerifyTLS:       false,
		VaultJournalDir:         "01_Projects/Journal",
		AudioRetentionDays:      14,
		TranscriptRetentionDays: 7,
		MaxRetryAttempts:        8,
		RetryBaseSeconds:        300,
		RetryMaxSeconds:         86400,
		StateDBPath:             dbPath,
		AudioStoreDir:           filepath.Join(tmp, "audio"),
		LogDir:                  filepath.Join(tmp, "logs"),
		LockFilePath:            dbPath + ".lock",
	}

	runner := New(
		cfg,
		st,
		discord.NewWithBaseURL(cfg.DiscordBotToken, cfg.DiscordAPIBaseURL),
		obsidian.New(cfg.ObsidianBaseURL, cfg.ObsidianAuthHeader, cfg.ObsidianAPIKey, cfg.ObsidianVerifyTLS),
	)

	cleanup := func() {
		_ = st.Close()
	}
	return runner, st, cfg, cleanup
}

func makeMessage(serverURL, messageID string) discord.Message {
	return discord.Message{
		ID:        messageID,
		ChannelID: "1476388224124325909",
		GuildID:   "g1",
		Author:    discord.User{ID: "968754117885456425"},
		Attachments: []discord.Attachment{{
			ID:          "att-" + messageID,
			URL:         fmt.Sprintf("%s/attachments/%s", serverURL, messageID),
			Filename:    "memo.ogg",
			ContentType: "audio/ogg",
		}},
	}
}

func makeTextMessage(messageID, content string) discord.Message {
	return discord.Message{
		ID:        messageID,
		ChannelID: "1476388224124325909",
		GuildID:   "g1",
		Content:   content,
		Author:    discord.User{ID: "968754117885456425"},
	}
}

func TestPollOnceSuccessAndNoDuplicate(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	dm.messages = []discord.Message{makeMessage(dm.server.URL, "1001")}
	runner, st, _, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	ctx := context.Background()
	res, err := runner.PollOnce(ctx)
	if err != nil {
		t.Fatalf("poll once failed: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetMessage("1001")
	if err != nil || !found {
		t.Fatalf("expected stored message: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected status done, got %s", rec.Status)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	if !strings.Contains(om.files[journalPath], "テスト文字起こし") {
		t.Fatalf("journal content missing transcript")
	}
	initialAppendHits := om.appendHits

	res2, err := runner.PollOnce(ctx)
	if err != nil {
		t.Fatalf("second poll should succeed: %v", err)
	}
	if res2.Succeeded != 0 {
		t.Fatalf("expected no new success on duplicate poll, got %+v", res2)
	}
	if om.appendHits != initialAppendHits {
		t.Fatalf("expected no duplicate append, before=%d after=%d", initialAppendHits, om.appendHits)
	}
}

func TestPollOnceObsidianAppendFailure(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()
	om.appendFail = true

	dm.messages = []discord.Message{makeMessage(dm.server.URL, "2001")}
	runner, st, _, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	res, err := runner.PollOnce(context.Background())
	if err == nil {
		t.Fatalf("expected partial/failed poll")
	}
	if res.Failed != 1 || res.Requeued != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetMessage("2001")
	if err != nil || !found {
		t.Fatalf("expected stored message: found=%v err=%v", found, err)
	}
	if rec.Status != "failed" {
		t.Fatalf("expected failed status, got %s", rec.Status)
	}
	if rec.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", rec.Attempts)
	}
	if rec.NextRetryAt == nil {
		t.Fatalf("expected next_retry_at to be set")
	}
}

func TestPollOnceProcessesDueRetries(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()
	om.appendFail = true

	dm.messages = []discord.Message{makeMessage(dm.server.URL, "2501")}
	runner, st, _, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	res, err := runner.PollOnce(context.Background())
	if err == nil {
		t.Fatalf("expected initial poll failure")
	}
	if res.Failed != 1 || res.Requeued != 1 {
		t.Fatalf("unexpected initial result: %+v", res)
	}

	rec, found, err := st.GetMessage("2501")
	if err != nil || !found {
		t.Fatalf("expected stored message after initial failure: found=%v err=%v", found, err)
	}

	past := time.Now().Add(-time.Minute)
	if err := st.MarkFailed(rec.MessageID, rec.LastError, rec.Attempts, &past); err != nil {
		t.Fatalf("force retry due: %v", err)
	}

	om.appendFail = false
	dm.messages = nil

	res, err = runner.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("poll should drain due retries: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected retry-draining result: %+v", res)
	}

	rec, found, err = st.GetMessage("2501")
	if err != nil || !found {
		t.Fatalf("expected retried message: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected done after due retry, got %s", rec.Status)
	}
}

func TestPollOnceReactionFailureGoesReactionPending(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()
	dm.reactionFail = true

	dm.messages = []discord.Message{makeMessage(dm.server.URL, "3001")}
	runner, st, _, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	res, err := runner.PollOnce(context.Background())
	if err == nil {
		t.Fatalf("expected partial/failed poll")
	}
	if res.Failed != 1 || res.Requeued != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetMessage("3001")
	if err != nil || !found {
		t.Fatalf("expected stored message: found=%v err=%v", found, err)
	}
	if rec.Status != "reaction_pending" {
		t.Fatalf("expected reaction_pending, got %s", rec.Status)
	}
	if rec.JournalPath == "" || rec.TranscriptPath == "" {
		t.Fatalf("expected artifacts to be saved for reaction retry")
	}
}

func TestPollOnceTextMessage(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	dm.messages = []discord.Message{makeTextMessage("4001", "これはテキスト入力です")}
	runner, st, _, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	res, err := runner.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("poll once failed for text message: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetMessage("4001")
	if err != nil || !found {
		t.Fatalf("expected stored text message: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected done status, got %s", rec.Status)
	}
	if rec.AudioPath != "" {
		t.Fatalf("expected empty audio path for text message, got %s", rec.AudioPath)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	if !strings.Contains(om.files[journalPath], "これはテキスト入力です") {
		t.Fatalf("journal should include text message content")
	}
	if !strings.Contains(om.files[journalPath], "<!-- vi:discord:4001 -->") {
		t.Fatalf("journal should include HTML comment marker")
	}
}

func TestProcessCapturesOnceAppendsJournalAndMarksDone(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	audioPath := filepath.Join(cfg.AudioStoreDir, "ingest", "memo.m4a")
	if err := os.MkdirAll(filepath.Dir(audioPath), 0o755); err != nil {
		t.Fatalf("mkdir capture dir: %v", err)
	}
	if err := os.WriteFile(audioPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write capture audio: %v", err)
	}

	capturedAt := time.Now().Add(-2 * time.Minute).UTC()
	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:       "cap-1001",
		Source:          "android-voice-inbox",
		SourceDedupeKey: "cap-1001",
		DeviceID:        "pixel-8a",
		CapturedAt:      &capturedAt,
		ReceivedAt:      time.Now().UTC(),
		RawAudioPath:    audioPath,
		ContentType:     "audio/mp4",
		Status:          "pending",
	}); err != nil {
		t.Fatalf("create capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("process captures once failed: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetCapture("cap-1001")
	if err != nil || !found {
		t.Fatalf("expected stored capture: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected done status, got %s", rec.Status)
	}
	if rec.JournalPath == "" || rec.TranscriptPath == "" {
		t.Fatalf("expected journal and transcript artifacts, got %+v", rec)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	content := om.files[journalPath]
	if !strings.Contains(content, "<!-- vi:android-voice-inbox:cap-1001 -->") {
		t.Fatalf("journal should include HTML capture marker")
	}
	if !strings.Contains(content, "via pixel-8a") {
		t.Fatalf("journal should include device label")
	}
}

func TestProcessCapturesOnceProcessesPendingCapture(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawPath := filepath.Join(cfg.AudioStoreDir, "ingest", "2026", "03", "19", "capture-5001.ogg")
	if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}
	capturedAt := time.Date(2026, 3, 19, 11, 0, 0, 0, time.UTC)
	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:    "capture-5001",
		Source:       "android-voice-inbox",
		DeviceID:     "pixel-8a",
		CapturedAt:   &capturedAt,
		ReceivedAt:   time.Now().UTC(),
		RawAudioPath: rawPath,
		ContentType:  "audio/ogg",
		Status:       "pending",
	}); err != nil {
		t.Fatalf("create capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("process captures once failed: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetCapture("capture-5001")
	if err != nil || !found {
		t.Fatalf("expected stored capture: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected done status, got %s", rec.Status)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	content := om.files[journalPath]
	if !strings.Contains(content, "<!-- vi:android-voice-inbox:capture-5001 -->") {
		t.Fatalf("journal should include capture marker")
	}
	if !strings.Contains(content, "テスト文字起こし") {
		t.Fatalf("journal should include transcript")
	}
}

func TestProcessCapturesOnceUsesPreTranscribedTextAndSkipsWhisper(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	cfg.WhisperBin = filepath.Join(t.TempDir(), "missing-whisper")
	runner = New(
		cfg,
		st,
		discord.NewWithBaseURL(cfg.DiscordBotToken, cfg.DiscordAPIBaseURL),
		obsidian.New(cfg.ObsidianBaseURL, cfg.ObsidianAuthHeader, cfg.ObsidianAPIKey, cfg.ObsidianVerifyTLS),
	)

	rawPath := filepath.Join(cfg.AudioStoreDir, "ingest", "2026", "03", "19", "capture-pretranscribed.ogg")
	if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}
	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:      "capture-pretranscribed",
		Source:         "android-voice-inbox",
		DeviceID:       "pixel-8a",
		ReceivedAt:     time.Now().UTC(),
		RawAudioPath:   rawPath,
		ContentType:    "audio/ogg",
		TranscriptText: "Android already transcribed this",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("create capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("process captures once failed: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetCapture("capture-pretranscribed")
	if err != nil || !found {
		t.Fatalf("expected stored capture: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected done status, got %s", rec.Status)
	}
	if rec.TranscriptPath != "" {
		t.Fatalf("expected no whisper transcript artifact for pre-transcribed capture, got %q", rec.TranscriptPath)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	content := om.files[journalPath]
	if !strings.Contains(content, "Android already transcribed this") {
		t.Fatalf("journal should include pre-transcribed text")
	}
	if !strings.Contains(content, "<!-- vi:android-voice-inbox:capture-pretranscribed -->") {
		t.Fatalf("journal should include HTML marker")
	}
}

func TestProcessCapturesOnceTreatsStoredRawFileAsAudioDespiteTextMime(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawPath := filepath.Join(cfg.AudioStoreDir, "ingest", "2026", "03", "19", "capture-plain-text.bin")
	if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}
	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:    "capture-plain-text",
		Source:       "android-voice-inbox",
		DeviceID:     "pixel-8a",
		ReceivedAt:   time.Now().UTC(),
		RawAudioPath: rawPath,
		ContentType:  "text/plain",
		Status:       "pending",
	}); err != nil {
		t.Fatalf("create capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("process captures once failed: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetCapture("capture-plain-text")
	if err != nil || !found {
		t.Fatalf("expected stored capture: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected done status, got %s", rec.Status)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	content := om.files[journalPath]
	if !strings.Contains(content, "テスト文字起こし") {
		t.Fatalf("journal should include transcript")
	}
	if !strings.Contains(content, "<!-- vi:android-voice-inbox:capture-plain-text -->") {
		t.Fatalf("journal should include HTML marker, got %s", content)
	}
}

func TestProcessCapturesOnceBlankTranscriptFallsBackToWhisper(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawPath := filepath.Join(cfg.AudioStoreDir, "ingest", "2026", "03", "19", "capture-blank-transcript.ogg")
	if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}
	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:      "capture-blank-transcript",
		Source:         "android-voice-inbox",
		DeviceID:       "pixel-8a",
		ReceivedAt:     time.Now().UTC(),
		RawAudioPath:   rawPath,
		ContentType:    "audio/ogg",
		TranscriptText: "   ",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("create capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("process captures once failed: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetCapture("capture-blank-transcript")
	if err != nil || !found {
		t.Fatalf("expected stored capture: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected done status, got %s", rec.Status)
	}
	if rec.TranscriptPath == "" {
		t.Fatalf("expected whisper transcript artifact for blank transcript fallback")
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	content := om.files[journalPath]
	if !strings.Contains(content, "テスト文字起こし") {
		t.Fatalf("journal should include whisper transcript")
	}
	if !strings.Contains(content, "<!-- vi:android-voice-inbox:capture-blank-transcript -->") {
		t.Fatalf("journal should include HTML marker for fallback path")
	}
}

func TestProcessTargetDefaultsUnknownMimeRawFileToAudio(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, _, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawPath := filepath.Join(cfg.AudioStoreDir, "ingest", "2026", "03", "19", "capture-octet-stream.bin")
	if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}

	artifacts, err := runner.processTarget(context.Background(), processTarget{
		Source:       "android-voice-inbox",
		CaptureID:    "capture-octet-stream",
		ContentType:  "application/octet-stream",
		RawAudioPath: rawPath,
	})
	if err != nil {
		t.Fatalf("process target failed: %v", err)
	}
	if artifacts.TranscriptPath == "" {
		t.Fatalf("expected transcript artifact, got %+v", artifacts)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	content := om.files[journalPath]
	if !strings.Contains(content, "テスト文字起こし") {
		t.Fatalf("journal should include transcript")
	}
	if !strings.Contains(content, "<!-- vi:android-voice-inbox:capture-octet-stream -->") {
		t.Fatalf("journal should include capture marker")
	}

	normalizedPath := filepath.Join(filepath.Dir(rawPath), "capture-octet-stream_16k.wav")
	if _, err := os.Stat(normalizedPath); !os.IsNotExist(err) {
		t.Fatalf("expected normalized wav cleanup, stat err=%v", err)
	}
}

func TestProcessTargetJournalDedupeIncludesSource(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, _, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawDir := filepath.Join(cfg.AudioStoreDir, "ingest", "2026", "03", "19")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}

	firstPath := filepath.Join(rawDir, "shared-id-discord.ogg")
	if err := os.WriteFile(firstPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write first raw audio: %v", err)
	}
	if _, err := runner.processTarget(context.Background(), processTarget{
		Source:       "discord",
		CaptureID:    "shared-id",
		ContentType:  "audio/ogg",
		RawAudioPath: firstPath,
	}); err != nil {
		t.Fatalf("process first target failed: %v", err)
	}

	secondPath := filepath.Join(rawDir, "shared-id-android.ogg")
	if err := os.WriteFile(secondPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write second raw audio: %v", err)
	}
	if _, err := runner.processTarget(context.Background(), processTarget{
		Source:       "android-voice-inbox",
		CaptureID:    "shared-id",
		ContentType:  "audio/ogg",
		RawAudioPath: secondPath,
	}); err != nil {
		t.Fatalf("process second target failed: %v", err)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	content := om.files[journalPath]
	if strings.Count(content, "<!-- vi:") != 2 {
		t.Fatalf("expected both entries to be appended, got %s", content)
	}
	if !strings.Contains(content, "<!-- vi:discord:shared-id -->") {
		t.Fatalf("journal should include discord capture key")
	}
	if !strings.Contains(content, "<!-- vi:android-voice-inbox:shared-id -->") {
		t.Fatalf("journal should include android capture key")
	}
	if strings.Contains(content, "```yaml") {
		t.Fatalf("journal should not include YAML metadata block")
	}
}

func TestProcessCapturesOnceRecoversProcessingCapture(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawPath := filepath.Join(cfg.AudioStoreDir, "ingest", "2026", "03", "19", "capture-5002.ogg")
	if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}
	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:    "capture-5002",
		Source:       "android-voice-inbox",
		DeviceID:     "pixel-8a",
		ReceivedAt:   time.Now().Add(-10 * time.Minute).UTC(),
		RawAudioPath: rawPath,
		ContentType:  "audio/ogg",
		Status:       "processing",
		Attempts:     1,
		UpdatedAt:    time.Now().Add(-10 * time.Minute).UTC(),
	}); err != nil {
		t.Fatalf("create processing capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("process captures once failed: %v", err)
	}
	if res.Succeeded != 0 || res.Failed != 0 || res.Processed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if recovered, ok := res.Data["recovered_captures"].(int); ok && recovered < 1 {
		t.Fatalf("expected recovered captures > 0, got %+v", res.Data)
	}

	rec, found, err := st.GetCapture("capture-5002")
	if err != nil || !found {
		t.Fatalf("expected recovered capture: found=%v err=%v", found, err)
	}
	if rec.Status != "failed" {
		t.Fatalf("expected recovered capture to be backoff-failed, got %s", rec.Status)
	}
	if rec.Attempts != 2 {
		t.Fatalf("expected attempts to increment, got %d", rec.Attempts)
	}
	if rec.NextRetryAt == nil || !rec.NextRetryAt.After(time.Now()) {
		t.Fatalf("expected next retry in the future, got %+v", rec.NextRetryAt)
	}

	past := time.Now().Add(-time.Minute)
	if err := st.MarkCaptureFailed(rec.CaptureID, rec.LastError, rec.Attempts, &past); err != nil {
		t.Fatalf("force retry due: %v", err)
	}

	res, err = runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("expected successful recovery retry pass: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected retry result: %+v", res)
	}

	rec, found, err = st.GetCapture("capture-5002")
	if err != nil || !found {
		t.Fatalf("expected recovered capture after retry: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected done after retry, got %s", rec.Status)
	}
}

func TestProcessCapturesOnceLeavesFreshProcessingCaptureUntouched(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawPath := filepath.Join(cfg.AudioStoreDir, "ingest", "2026", "03", "19", "capture-fresh-processing.ogg")
	if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}
	updatedAt := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Second)
	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:    "capture-fresh-processing",
		Source:       "android-voice-inbox",
		DeviceID:     "pixel-8a",
		ReceivedAt:   time.Now().UTC(),
		RawAudioPath: rawPath,
		ContentType:  "audio/ogg",
		Status:       "processing",
		Attempts:     3,
		UpdatedAt:    updatedAt,
	}); err != nil {
		t.Fatalf("create processing capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("process captures once failed: %v", err)
	}
	if recovered, ok := res.Data["recovered_captures"]; ok {
		t.Fatalf("expected no recovered captures, got %+v", recovered)
	}

	rec, found, err := st.GetCapture("capture-fresh-processing")
	if err != nil || !found {
		t.Fatalf("expected fresh processing capture: found=%v err=%v", found, err)
	}
	if rec.Status != "processing" {
		t.Fatalf("expected status to remain processing, got %s", rec.Status)
	}
	if rec.Attempts != 3 {
		t.Fatalf("expected attempts unchanged, got %d", rec.Attempts)
	}
	if !rec.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("expected updated_at unchanged, got %s", rec.UpdatedAt)
	}
}

func TestProcessCapturesOnceSuccessAndNoDuplicateJournalAppend(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawDir := filepath.Join(cfg.AudioStoreDir, "http", "2026", "03", "19")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	rawPath := filepath.Join(rawDir, "cap-2001.ogg")
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}

	capturedAt := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:       "cap-2001",
		Source:          "android-voice-inbox",
		SourceDedupeKey: "cap-2001",
		DeviceID:        "pixel-8a",
		CapturedAt:      &capturedAt,
		ReceivedAt:      time.Now().UTC(),
		RawAudioPath:    rawPath,
		ContentType:     "audio/ogg",
		Status:          "pending",
	}); err != nil {
		t.Fatalf("create capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("process captures failed: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetCapture("cap-2001")
	if err != nil || !found {
		t.Fatalf("expected stored capture: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected capture done, got %s", rec.Status)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	if !strings.Contains(om.files[journalPath], "<!-- vi:android-voice-inbox:cap-2001 -->") {
		t.Fatalf("journal should include capture marker")
	}
	appendHits := om.appendHits

	res, err = runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("second capture processing pass should succeed: %v", err)
	}
	if res.Succeeded != 0 || om.appendHits != appendHits {
		t.Fatalf("expected no duplicate append, result=%+v appendHits=%d", res, om.appendHits)
	}
}

func TestProcessCapturesOnceRetriesFailedCapture(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()
	om.appendFail = true

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawDir := filepath.Join(cfg.AudioStoreDir, "http", "2026", "03", "19")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	rawPath := filepath.Join(rawDir, "cap-3001.ogg")
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}

	capturedAt := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:       "cap-3001",
		Source:          "android-voice-inbox",
		SourceDedupeKey: "cap-3001",
		DeviceID:        "pixel-8a",
		CapturedAt:      &capturedAt,
		ReceivedAt:      time.Now().UTC(),
		RawAudioPath:    rawPath,
		ContentType:     "audio/ogg",
		Status:          "pending",
	}); err != nil {
		t.Fatalf("create capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err == nil {
		t.Fatalf("expected failed processing pass")
	}
	if res.Failed != 1 || res.Requeued != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}

	rec, found, err := st.GetCapture("cap-3001")
	if err != nil || !found {
		t.Fatalf("expected stored capture after failure: found=%v err=%v", found, err)
	}
	if rec.Status != "failed" || rec.NextRetryAt == nil {
		t.Fatalf("expected failed capture with retry, got %+v", rec)
	}

	past := time.Now().Add(-time.Minute)
	if err := st.MarkCaptureFailed(rec.CaptureID, rec.LastError, rec.Attempts, &past); err != nil {
		t.Fatalf("force retry due: %v", err)
	}
	om.appendFail = false

	res, err = runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("expected successful recovery pass: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected retry result: %+v", res)
	}

	rec, found, err = st.GetCapture("cap-3001")
	if err != nil || !found {
		t.Fatalf("expected stored capture after retry: found=%v err=%v", found, err)
	}
	if rec.Status != "done" {
		t.Fatalf("expected done after retry, got %s", rec.Status)
	}
}

func TestProcessCapturesOnceDedupsWithNewHTMLMarker(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawDir := filepath.Join(cfg.AudioStoreDir, "http", "2026", "03", "19")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	rawPath := filepath.Join(rawDir, "cap-html.ogg")
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	om.files[journalPath] = journal.NewJournalContent(time.Now()) + "\n## ログ - 12:00\n### 🎤 Voice Inbox\n\n既存エントリ\n\n_12:00 via Pixel 8a_\n<!-- vi:android-voice-inbox:cap-html -->\n"

	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:       "cap-html",
		Source:          "android-voice-inbox",
		SourceDedupeKey: "cap-html",
		DeviceID:        "Pixel 8a",
		ReceivedAt:      time.Now().UTC(),
		RawAudioPath:    rawPath,
		ContentType:     "audio/ogg",
		Status:          "pending",
	}); err != nil {
		t.Fatalf("create capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("process captures failed: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if om.appendHits != 0 {
		t.Fatalf("expected dedup to skip append, appendHits=%d", om.appendHits)
	}
	if strings.Count(om.files[journalPath], "<!-- vi:android-voice-inbox:cap-html -->") != 1 {
		t.Fatalf("expected exactly one HTML marker, got %q", om.files[journalPath])
	}
}

func TestProcessCapturesOnceDedupsWithOldYAMLMarker(t *testing.T) {
	dm := newDiscordMock(t)
	defer dm.close()
	om := newObsidianMock(t)
	defer om.close()

	runner, st, cfg, cleanup := setupRunner(t, dm, om)
	defer cleanup()

	rawDir := filepath.Join(cfg.AudioStoreDir, "http", "2026", "03", "19")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	rawPath := filepath.Join(rawDir, "cap-yaml.ogg")
	if err := os.WriteFile(rawPath, []byte("FAKE_AUDIO"), 0o644); err != nil {
		t.Fatalf("write raw audio: %v", err)
	}

	journalPath := "01_Projects/Journal/" + time.Now().Format("2006-01-02") + ".md"
	om.files[journalPath] = journal.NewJournalContent(time.Now()) + "\n## ログ - 12:00\n### 🎤 Voice Inbox\n\n既存エントリ\n\n```yaml\nvoice_inbox:\n  capture_key: \"android-voice-inbox:cap-yaml\"\n```\n"

	if err := st.CreateCapture(state.CaptureRecord{
		CaptureID:       "cap-yaml",
		Source:          "android-voice-inbox",
		SourceDedupeKey: "cap-yaml",
		DeviceID:        "Pixel 8a",
		ReceivedAt:      time.Now().UTC(),
		RawAudioPath:    rawPath,
		ContentType:     "audio/ogg",
		Status:          "pending",
	}); err != nil {
		t.Fatalf("create capture: %v", err)
	}

	res, err := runner.ProcessCapturesOnce(context.Background())
	if err != nil {
		t.Fatalf("process captures failed: %v", err)
	}
	if res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if om.appendHits != 0 {
		t.Fatalf("expected dedup to skip append, appendHits=%d", om.appendHits)
	}
	if strings.Count(om.files[journalPath], `capture_key: "android-voice-inbox:cap-yaml"`) != 1 {
		t.Fatalf("expected exactly one YAML marker, got %q", om.files[journalPath])
	}
}
