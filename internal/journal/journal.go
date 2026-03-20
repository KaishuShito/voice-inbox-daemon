package journal

import (
	"fmt"
	"path"
	"strings"
	"time"
)

type EntryInput struct {
	Now          time.Time
	Transcript   string
	Source       string
	CaptureID    string
	DeviceID     string
	CapturedAt   *time.Time
	ChannelID    string
	MessageID    string
	AuthorID     string
	JumpURL      string
	AudioFile    string
	WhisperModel string
	ProcessedAt  time.Time
}

func FilePath(journalDir string, t time.Time) string {
	return path.Join(strings.Trim(journalDir, "/"), t.Format("2006-01-02")+".md")
}

func NewJournalContent(t time.Time) string {
	titleUnderscore := t.Format("2006_01_02")
	date := t.Format("2006-01-02")
	created := t.Format(time.RFC3339)
	return fmt.Sprintf(`---
title: "%s"
type: journal
date: %s
created: %s
tags: [journal]
source: voice-inbox-daemon
---
# %s
`, titleUnderscore, date, created, titleUnderscore)
}

func BuildEntry(in EntryInput) string {
	headlineTime := in.Now.Format("15:04")
	processedAt := in.ProcessedAt.Format(time.RFC3339)

	transcript := strings.TrimSpace(in.Transcript)
	if transcript == "" {
		transcript = "(transcript is empty)"
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = "discord"
	}
	captureKey := CaptureKey(source, in.CaptureID)
	capturedAt := ""
	if in.CapturedAt != nil && !in.CapturedAt.IsZero() {
		capturedAt = in.CapturedAt.UTC().Format(time.RFC3339)
	}

	return fmt.Sprintf(
		"\n## ログ - %s\n### 🎤 Voice Inbox\n\n%s\n\n```yaml\nvoice_inbox:\n  source: \"%s\"\n  capture_id: \"%s\"\n  capture_key: \"%s\"\n  device_id: \"%s\"\n  captured_at: \"%s\"\n  discord_channel_id: \"%s\"\n  discord_message_id: \"%s\"\n  discord_author_id: \"%s\"\n  discord_jump_url: \"%s\"\n  audio_file: \"%s\"\n  whisper_model: \"%s\"\n  processed_at: \"%s\"\n```\n",
		headlineTime,
		transcript,
		source,
		in.CaptureID,
		captureKey,
		in.DeviceID,
		capturedAt,
		in.ChannelID,
		in.MessageID,
		in.AuthorID,
		in.JumpURL,
		in.AudioFile,
		in.WhisperModel,
		processedAt,
	)
}

func CaptureKey(source, captureID string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "discord"
	}
	return source + ":" + strings.TrimSpace(captureID)
}

func DiscordJumpURL(guildID, channelID, messageID string) string {
	return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, channelID, messageID)
}
