package journal

import (
	"fmt"
	"path"
	"strings"
	"time"
)

type EntryInput struct {
	Now        time.Time
	Transcript string
	Source     string
	CaptureID  string
	DeviceID   string
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

	transcript := strings.TrimSpace(in.Transcript)
	if transcript == "" {
		transcript = "(transcript is empty)"
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = "discord"
	}
	captureKey := CaptureKey(source, in.CaptureID)
	label := strings.TrimSpace(in.DeviceID)
	if label == "" {
		switch source {
		case "discord":
			label = "Discord"
		default:
			label = source
		}
	}

	return fmt.Sprintf(
		"\n## ログ - %s\n### 🎤 Voice Inbox\n\n%s\n\n_%s via %s_\n<!-- vi:%s -->\n",
		headlineTime,
		transcript,
		headlineTime,
		label,
		captureKey,
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
