package pipeline

import (
	"sort"
	"strings"

	"voice-inbox-daemon/internal/discord"
	"voice-inbox-daemon/internal/journal"
)

type CandidateKind string

const (
	CandidateKindAudio CandidateKind = "audio"
	CandidateKindText  CandidateKind = "text"
)

type Candidate struct {
	Message    discord.Message
	Attachment discord.Attachment
	Kind       CandidateKind
	JumpURL    string
}

func FilterMessages(messages []discord.Message, allowedAuthorIDs map[string]struct{}) []Candidate {
	sorted := append([]discord.Message(nil), messages...)
	sort.Slice(sorted, func(i, j int) bool {
		return snowflakeCompare(sorted[i].ID, sorted[j].ID) < 0
	})

	out := make([]Candidate, 0, len(sorted))
	for _, msg := range sorted {
		if _, ok := allowedAuthorIDs[msg.Author.ID]; !ok {
			continue
		}

		kind := CandidateKind("")
		att := discord.Attachment{}
		if a, ok := firstAudioAttachment(msg.Attachments); ok {
			kind = CandidateKindAudio
			att = a
		} else if strings.TrimSpace(msg.Content) != "" {
			kind = CandidateKindText
			att = discord.Attachment{
				ID:          "text-" + msg.ID,
				URL:         "about:text",
				Filename:    "message.txt",
				ContentType: "text/plain",
			}
		}
		if kind == "" {
			continue
		}

		jump := ""
		if msg.GuildID != "" {
			jump = journal.DiscordJumpURL(msg.GuildID, msg.ChannelID, msg.ID)
		}
		out = append(out, Candidate{
			Message:    msg,
			Attachment: att,
			Kind:       kind,
			JumpURL:    jump,
		})
	}
	return out
}

func firstAudioAttachment(attachments []discord.Attachment) (discord.Attachment, bool) {
	for _, a := range attachments {
		if discord.IsAudioContentType(a.ContentType) {
			return a, true
		}
	}
	return discord.Attachment{}, false
}

func snowflakeCompare(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
