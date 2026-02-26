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

func TestBuildEntry(t *testing.T) {
	now := time.Date(2026, 2, 26, 15, 42, 1, 0, time.UTC)
	entry := BuildEntry(EntryInput{
		Now:          now,
		Transcript:   "テスト音声",
		ChannelID:    "1476388224124325909",
		MessageID:    "123",
		AuthorID:     "968754117885456425",
		JumpURL:      "https://discord.com/channels/g/c/m",
		AudioFile:    "2026/02/26/123_abc.orig",
		WhisperModel: "large-v3-turbo",
		ProcessedAt:  now,
	})

	checks := []string{
		"## ログ - 15:42",
		"### 🎤 Voice Inbox",
		"テスト音声",
		"voice_inbox:",
		`discord_message_id: "123"`,
		`audio_file: "2026/02/26/123_abc.orig"`,
	}
	for _, c := range checks {
		if !strings.Contains(entry, c) {
			t.Fatalf("expected entry to include %q", c)
		}
	}
}
