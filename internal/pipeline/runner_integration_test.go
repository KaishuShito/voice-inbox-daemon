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
