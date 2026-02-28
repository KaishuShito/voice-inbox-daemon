package pipeline

import (
	"testing"

	"voice-inbox-daemon/internal/discord"
)

func TestFilterMessages(t *testing.T) {
	allowed := map[string]struct{}{"968754117885456425": {}}

	messages := []discord.Message{
		{
			ID:        "200",
			ChannelID: "ch",
			GuildID:   "guild",
			Author:    discord.User{ID: "blocked"},
			Attachments: []discord.Attachment{{
				ID:          "att1",
				URL:         "https://example.com/1",
				Filename:    "memo.ogg",
				ContentType: "audio/ogg",
			}},
		},
		{
			ID:        "300",
			ChannelID: "ch",
			GuildID:   "guild",
			Content:   "text only memo",
			Author:    discord.User{ID: "968754117885456425"},
			Attachments: []discord.Attachment{{
				ID:          "att2",
				URL:         "https://example.com/2",
				Filename:    "memo.txt",
				ContentType: "text/plain",
			}},
		},
		{
			ID:        "100",
			ChannelID: "ch",
			GuildID:   "guild",
			Author:    discord.User{ID: "968754117885456425"},
			Attachments: []discord.Attachment{{
				ID:          "att3",
				URL:         "https://example.com/3",
				Filename:    "memo.ogg",
				ContentType: "audio/ogg",
			}},
		},
	}

	candidates := FilterMessages(messages, allowed)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
	if candidates[0].Message.ID != "100" {
		t.Fatalf("expected oldest sorted id 100, got %s", candidates[0].Message.ID)
	}
	if candidates[0].Kind != CandidateKindAudio {
		t.Fatalf("expected first candidate kind audio, got %s", candidates[0].Kind)
	}
	if candidates[0].Attachment.ID != "att3" {
		t.Fatalf("expected att3, got %s", candidates[0].Attachment.ID)
	}
	if candidates[1].Message.ID != "300" {
		t.Fatalf("expected second candidate id 300, got %s", candidates[1].Message.ID)
	}
	if candidates[1].Kind != CandidateKindText {
		t.Fatalf("expected second candidate kind text, got %s", candidates[1].Kind)
	}
}
