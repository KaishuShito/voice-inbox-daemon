package journal

import (
	"strings"
	"testing"
	"time"
)

func TestNewJournalContent(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	now := time.Date(2026, 2, 26, 15, 42, 1, 0, jst)

	content := NewJournalContent(now)
	checks := []string{
		`title: "2026_02_26"`,
		"date: 2026-02-26",
		"source: voice-inbox-daemon",
		"# 2026_02_26",
	}
	for _, c := range checks {
		if !strings.Contains(content, c) {
			t.Fatalf("expected content to include %q", c)
		}
	}
}

func TestBuildEntryUsesDeviceIDLabel(t *testing.T) {
	now := time.Date(2026, 2, 26, 15, 42, 1, 0, time.UTC)
	entry := BuildEntry(EntryInput{
		Now:        now,
		Transcript: "テスト音声",
		Source:     "android-voice-inbox",
		CaptureID:  "123",
		DeviceID:   "Pixel 8a",
	})

	checks := []string{
		"## ログ - 15:42",
		"### 🎤 Voice Inbox",
		"テスト音声",
		"_15:42 via Pixel 8a_",
		"<!-- vi:android-voice-inbox:123 -->",
	}
	for _, c := range checks {
		if !strings.Contains(entry, c) {
			t.Fatalf("expected entry to include %q", c)
		}
	}
	for _, c := range []string{"```yaml", "voice_inbox:", "capture_key:"} {
		if strings.Contains(entry, c) {
			t.Fatalf("expected entry to omit %q", c)
		}
	}
}

func TestBuildEntryUsesDiscordLabelWhenDeviceIDMissing(t *testing.T) {
	now := time.Date(2026, 2, 26, 15, 42, 1, 0, time.UTC)
	entry := BuildEntry(EntryInput{
		Now:        now,
		Transcript: "テスト音声",
		Source:     "discord",
		CaptureID:  "123",
	})

	if !strings.Contains(entry, "_15:42 via Discord_") {
		t.Fatalf("expected Discord label, got %q", entry)
	}
	if !strings.Contains(entry, "<!-- vi:discord:123 -->") {
		t.Fatalf("expected HTML marker, got %q", entry)
	}
}

func TestBuildEntryUsesSourceLabelWhenDeviceIDMissing(t *testing.T) {
	now := time.Date(2026, 2, 26, 15, 42, 1, 0, time.UTC)
	entry := BuildEntry(EntryInput{
		Now:        now,
		Transcript: "テスト音声",
		Source:     "android-voice-inbox",
		CaptureID:  "123",
	})

	if !strings.Contains(entry, "_15:42 via android-voice-inbox_") {
		t.Fatalf("expected source label, got %q", entry)
	}
}
