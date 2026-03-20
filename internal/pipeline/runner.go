package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"voice-inbox-daemon/internal/config"
	"voice-inbox-daemon/internal/discord"
	"voice-inbox-daemon/internal/journal"
	"voice-inbox-daemon/internal/obsidian"
	"voice-inbox-daemon/internal/state"
	"voice-inbox-daemon/internal/transcribe"
)

const checkMarkEmojiEscaped = "%E2%9C%85"

type Runner struct {
	cfg      config.Config
	store    *state.Store
	discord  *discord.Client
	obsidian *obsidian.Client
}

type processTarget struct {
	Source             string
	CaptureID          string
	DeviceID           string
	CapturedAt         *time.Time
	PreTranscribedText string
	Kind               CandidateKind
	TextContent        string
	ChannelID          string
	MessageID          string
	AuthorID           string
	GuildID            string
	JumpURL            string
	AttachmentID       string
	AttachmentURL      string
	AttachmentName     string
	ContentType        string
	RawAudioPath       string
}

type processArtifacts struct {
	JournalPath    string
	RawAudioPath   string
	TranscriptPath string
}

func New(cfg config.Config, store *state.Store, discordClient *discord.Client, obsidianClient *obsidian.Client) *Runner {
	return &Runner{
		cfg:      cfg,
		store:    store,
		discord:  discordClient,
		obsidian: obsidianClient,
	}
}

func (r *Runner) Doctor(ctx context.Context) (Result, error) {
	started := time.Now()
	res := Result{Command: "doctor", Data: map[string]any{}}

	type check struct {
		Name   string `json:"name"`
		Pass   bool   `json:"pass"`
		Detail string `json:"detail"`
	}
	checks := make([]check, 0, 8)
	failures := 0

	addCheck := func(name string, err error, okDetail string) {
		if err != nil {
			failures++
			checks = append(checks, check{Name: name, Pass: false, Detail: err.Error()})
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", name, err))
			return
		}
		checks = append(checks, check{Name: name, Pass: true, Detail: okDetail})
	}

	addCheck("whisper_bin", checkExecutable(r.cfg.WhisperBin), "found")
	addCheck("ffmpeg_bin", checkExecutable(r.cfg.FFmpegBin), "found")
	addCheck("db_writable", r.store.SetKV("doctor_last_run", time.Now().UTC().Format(time.RFC3339)), "ok")

	if me, err := r.discord.Me(ctx); err != nil {
		addCheck("discord_api", err, "")
	} else {
		addCheck("discord_api", nil, fmt.Sprintf("authenticated as %s (%s)", me.Username, me.ID))
	}

	if health, err := r.obsidian.Health(ctx); err != nil {
		addCheck("obsidian_api", err, "")
	} else if !health.Authenticated {
		addCheck("obsidian_api", errors.New("authenticated=false"), "")
	} else {
		addCheck("obsidian_api", nil, "authenticated=true")
	}

	res.Data["checks"] = checks
	res.Failed = failures
	finalizeResult(&res, started)
	if failures > 0 {
		return res, errors.New("doctor checks failed")
	}
	return res, nil
}

func (r *Runner) PollOnce(ctx context.Context) (Result, error) {
	started := time.Now()
	res := Result{Command: "poll"}

	lock, err := state.AcquireFileLock(r.cfg.LockFilePath)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		res.Failed = 1
		finalizeResult(&res, started)
		return res, err
	}
	defer lock.Release()

	runID, err := r.store.BeginRun("poll", started)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		res.Failed = 1
		finalizeResult(&res, started)
		return res, err
	}
	res.RunID = runID
	defer func() {
		_ = r.store.FinishRun(runID, time.Now(), res.Processed, res.Succeeded, res.Failed)
	}()

	lastSeen, _, err := r.store.GetKV("last_seen_message_id")
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("read last_seen_message_id: %v", err))
	}

	messages, err := r.discord.FetchMessages(ctx, r.cfg.VoiceInboxChannelID, lastSeen, r.cfg.DiscordFetchLimit)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		res.Failed = 1
		finalizeResult(&res, started)
		return res, err
	}

	maxSeen := lastSeen
	for _, m := range messages {
		if maxSeen == "" || snowflakeCompare(m.ID, maxSeen) > 0 {
			maxSeen = m.ID
		}
	}

	candidates := FilterMessages(messages, r.cfg.AllowedAuthorIDs)
	fallbackGuildID := ""
	for _, m := range messages {
		if strings.TrimSpace(m.GuildID) != "" {
			fallbackGuildID = m.GuildID
			break
		}
	}
	if fallbackGuildID == "" {
		if ch, chErr := r.discord.GetChannel(ctx, r.cfg.VoiceInboxChannelID); chErr == nil {
			fallbackGuildID = ch.GuildID
		}
	}
	if fallbackGuildID != "" {
		for i := range candidates {
			if candidates[i].JumpURL == "" {
				candidates[i].JumpURL = journal.DiscordJumpURL(fallbackGuildID, candidates[i].Message.ChannelID, candidates[i].Message.ID)
			}
		}
	}
	for _, c := range candidates {
		rec, found, getErr := r.store.GetMessage(c.Message.ID)
		if getErr != nil {
			res.Processed++
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("message %s lookup: %v", c.Message.ID, getErr))
			continue
		}
		if found && rec.Status == "done" {
			res.Skipped++
			continue
		}

		recUpsert := state.MessageRecord{
			MessageID:          c.Message.ID,
			ChannelID:          c.Message.ChannelID,
			AuthorID:           c.Message.Author.ID,
			AttachmentID:       c.Attachment.ID,
			AttachmentURL:      c.Attachment.URL,
			AttachmentFilename: c.Attachment.Filename,
			ContentType:        c.Attachment.ContentType,
			MessageContent:     c.Message.Content,
			DiscordJumpURL:     c.JumpURL,
		}
		if err := r.store.UpsertPending(recUpsert); err != nil {
			res.Processed++
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("message %s upsert: %v", c.Message.ID, err))
			continue
		}

		attempts := 0
		if found {
			attempts = rec.Attempts
		}

		res.Processed++
		succeeded, requeued, procErr := r.processCandidate(ctx, c, attempts)
		if procErr != nil {
			res.Failed++
			if requeued {
				res.Requeued++
			}
			res.Errors = append(res.Errors, fmt.Sprintf("message %s: %v", c.Message.ID, procErr))
			continue
		}
		if succeeded {
			res.Succeeded++
		}
	}

	if maxSeen != "" && maxSeen != lastSeen {
		if err := r.store.SetKV("last_seen_message_id", maxSeen); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("update last_seen_message_id: %v", err))
		}
	}

	retryCandidates, err := r.store.ListRetryCandidates(time.Now(), r.cfg.DiscordFetchLimit)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("list retry candidates: %v", err))
		res.Failed++
	} else {
		r.processRetryCandidates(ctx, retryCandidates, &res)
	}
	r.processReadyCaptures(ctx, &res)

	finalizeResult(&res, started)
	if res.Failed > 0 {
		return res, errors.New("poll completed with failures")
	}
	return res, nil
}

func (r *Runner) Retry(ctx context.Context) (Result, error) {
	started := time.Now()
	res := Result{Command: "retry"}

	lock, err := state.AcquireFileLock(r.cfg.LockFilePath)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		res.Failed = 1
		finalizeResult(&res, started)
		return res, err
	}
	defer lock.Release()

	runID, err := r.store.BeginRun("retry", started)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		res.Failed = 1
		finalizeResult(&res, started)
		return res, err
	}
	res.RunID = runID
	defer func() {
		_ = r.store.FinishRun(runID, time.Now(), res.Processed, res.Succeeded, res.Failed)
	}()

	candidates, err := r.store.ListRetryCandidates(time.Now(), r.cfg.DiscordFetchLimit)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		res.Failed = 1
		finalizeResult(&res, started)
		return res, err
	}

	r.processRetryCandidates(ctx, candidates, &res)
	r.processReadyCaptures(ctx, &res)

	finalizeResult(&res, started)
	if res.Failed > 0 {
		return res, errors.New("retry completed with failures")
	}
	return res, nil
}

func (r *Runner) ProcessCapturesOnce(ctx context.Context) (Result, error) {
	started := time.Now()
	res := Result{Command: "serve-process-captures"}

	lock, err := state.AcquireFileLock(r.cfg.LockFilePath)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		res.Failed = 1
		finalizeResult(&res, started)
		return res, err
	}
	defer lock.Release()

	runID, err := r.store.BeginRun("serve-process-captures", started)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		res.Failed = 1
		finalizeResult(&res, started)
		return res, err
	}
	res.RunID = runID
	defer func() {
		_ = r.store.FinishRun(runID, time.Now(), res.Processed, res.Succeeded, res.Failed)
	}()

	r.processReadyCaptures(ctx, &res)

	finalizeResult(&res, started)
	if res.Failed > 0 {
		return res, errors.New("capture processing completed with failures")
	}
	return res, nil
}

func (r *Runner) processRetryCandidates(ctx context.Context, candidates []state.MessageRecord, res *Result) {
	for _, rec := range candidates {
		res.Processed++
		if rec.Status == "reaction_pending" {
			if err := r.discord.AddReaction(ctx, rec.ChannelID, rec.MessageID, checkMarkEmojiEscaped); err != nil {
				attempts := rec.Attempts + 1
				if attempts >= r.cfg.MaxRetryAttempts {
					if markErr := r.store.MarkFailed(rec.MessageID, err.Error(), attempts, nil); markErr != nil {
						res.Errors = append(res.Errors, fmt.Sprintf("message %s mark permanent failed: %v", rec.MessageID, markErr))
					}
					res.Failed++
					res.Errors = append(res.Errors, fmt.Sprintf("message %s reaction retry exhausted: %v", rec.MessageID, err))
					continue
				}
				next := NextRetryAt(time.Now(), attempts, r.cfg.RetryBaseSeconds, r.cfg.RetryMaxSeconds)
				if markErr := r.store.MarkReactionPending(
					rec.MessageID,
					err.Error(),
					attempts,
					next,
					rec.JournalPath,
					rec.AudioPath,
					rec.TranscriptPath,
					rec.DiscordJumpURL,
				); markErr != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("message %s mark reaction_pending: %v", rec.MessageID, markErr))
				}
				res.Failed++
				res.Requeued++
				res.Errors = append(res.Errors, fmt.Sprintf("message %s reaction retry: %v", rec.MessageID, err))
				continue
			}

			if err := r.store.MarkDone(rec.MessageID, rec.JournalPath, rec.AudioPath, rec.TranscriptPath, rec.DiscordJumpURL); err != nil {
				res.Failed++
				res.Errors = append(res.Errors, fmt.Sprintf("message %s mark done after reaction retry: %v", rec.MessageID, err))
				continue
			}
			res.Succeeded++
			continue
		}

		if rec.Attempts >= r.cfg.MaxRetryAttempts {
			res.Skipped++
			continue
		}

		c := Candidate{
			Message: discord.Message{
				ID:        rec.MessageID,
				ChannelID: rec.ChannelID,
				Content:   rec.MessageContent,
				Author:    discord.User{ID: rec.AuthorID},
			},
			Attachment: discord.Attachment{
				ID:          rec.AttachmentID,
				URL:         rec.AttachmentURL,
				Filename:    rec.AttachmentFilename,
				ContentType: rec.ContentType,
			},
			Kind:    kindFromContentType(rec.ContentType),
			JumpURL: rec.DiscordJumpURL,
		}
		succeeded, requeued, procErr := r.processCandidate(ctx, c, rec.Attempts)
		if procErr != nil {
			res.Failed++
			if requeued {
				res.Requeued++
			}
			res.Errors = append(res.Errors, fmt.Sprintf("message %s retry: %v", rec.MessageID, procErr))
			continue
		}
		if succeeded {
			res.Succeeded++
		}
	}
}

func (r *Runner) processReadyCaptures(ctx context.Context, res *Result) {
	recovered, err := r.store.RecoverStuckCaptures(time.Now(), 5*time.Minute, r.cfg.RetryBaseSeconds, r.cfg.RetryMaxSeconds)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("recover stuck captures: %v", err))
		res.Failed++
		return
	}
	if recovered > 0 {
		res.Data = ensureData(res.Data)
		res.Data["recovered_captures"] = recovered
	}

	captures, err := r.store.ListCapturesForProcessing(time.Now(), r.cfg.DiscordFetchLimit)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("list captures for processing: %v", err))
		res.Failed++
		return
	}
	for _, rec := range captures {
		res.Processed++
		if err := r.store.MarkCaptureProcessing(rec.CaptureID); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("capture %s mark processing: %v", rec.CaptureID, err))
			continue
		}

		succeeded, requeued, procErr := r.processStoredCapture(ctx, rec)
		if procErr != nil {
			res.Failed++
			if requeued {
				res.Requeued++
			}
			res.Errors = append(res.Errors, fmt.Sprintf("capture %s: %v", rec.CaptureID, procErr))
			continue
		}
		if succeeded {
			res.Succeeded++
		}
	}
}

func (r *Runner) Cleanup(ctx context.Context) (Result, error) {
	_ = ctx
	started := time.Now()
	res := Result{Command: "cleanup", Data: map[string]any{}}

	lock, err := state.AcquireFileLock(r.cfg.LockFilePath)
	if err != nil {
		res.Failed = 1
		res.Errors = append(res.Errors, err.Error())
		finalizeResult(&res, started)
		return res, err
	}
	defer lock.Release()

	runID, err := r.store.BeginRun("cleanup", started)
	if err == nil {
		res.RunID = runID
		defer func() {
			_ = r.store.FinishRun(runID, time.Now(), res.Processed, res.Succeeded, res.Failed)
		}()
	}

	audioCutoff := time.Now().AddDate(0, 0, -r.cfg.AudioRetentionDays)
	audioRows, err := r.store.ListDoneWithAudioBefore(audioCutoff, 1000)
	if err != nil {
		res.Failed++
		res.Errors = append(res.Errors, fmt.Sprintf("list audio cleanup rows: %v", err))
		finalizeResult(&res, started)
		return res, err
	}

	audioRemoved := 0
	for _, rec := range audioRows {
		if rec.AudioPath == "" {
			continue
		}
		res.Processed++
		err := safeRemoveWithin(rec.AudioPath, r.cfg.AudioStoreDir)
		if err != nil && !os.IsNotExist(err) {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("remove audio %s: %v", rec.AudioPath, err))
			continue
		}
		if err := r.store.ClearAudioPath(rec.MessageID); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("clear audio path for %s: %v", rec.MessageID, err))
			continue
		}
		audioRemoved++
		res.Succeeded++
	}

	transcriptCutoff := time.Now().AddDate(0, 0, -r.cfg.TranscriptRetentionDays)
	transcriptRows, err := r.store.ListDoneWithTranscriptBefore(transcriptCutoff, 1000)
	if err != nil {
		res.Failed++
		res.Errors = append(res.Errors, fmt.Sprintf("list transcript cleanup rows: %v", err))
		finalizeResult(&res, started)
		return res, err
	}

	transcriptRemoved := 0
	for _, rec := range transcriptRows {
		if rec.TranscriptPath == "" {
			continue
		}
		res.Processed++
		err := safeRemoveWithin(rec.TranscriptPath, r.cfg.AudioStoreDir)
		if err != nil && !os.IsNotExist(err) {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("remove transcript %s: %v", rec.TranscriptPath, err))
			continue
		}
		if err := r.store.ClearTranscriptPath(rec.MessageID); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("clear transcript path for %s: %v", rec.MessageID, err))
			continue
		}
		transcriptRemoved++
		res.Succeeded++
	}

	res.Data["audio_removed"] = audioRemoved
	res.Data["transcript_removed"] = transcriptRemoved

	captureAudioRows, err := r.store.ListDoneCapturesWithAudioBefore(audioCutoff, 1000)
	if err != nil {
		res.Failed++
		res.Errors = append(res.Errors, fmt.Sprintf("list capture audio cleanup rows: %v", err))
		finalizeResult(&res, started)
		return res, err
	}

	captureAudioRemoved := 0
	for _, rec := range captureAudioRows {
		if rec.RawAudioPath == "" {
			continue
		}
		res.Processed++
		err := safeRemoveWithin(rec.RawAudioPath, r.cfg.AudioStoreDir)
		if err != nil && !os.IsNotExist(err) {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("remove capture audio %s: %v", rec.RawAudioPath, err))
			continue
		}
		if err := r.store.ClearCaptureAudioPath(rec.CaptureID); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("clear capture audio path for %s: %v", rec.CaptureID, err))
			continue
		}
		captureAudioRemoved++
		res.Succeeded++
	}

	captureTranscriptRows, err := r.store.ListDoneCapturesWithTranscriptBefore(transcriptCutoff, 1000)
	if err != nil {
		res.Failed++
		res.Errors = append(res.Errors, fmt.Sprintf("list capture transcript cleanup rows: %v", err))
		finalizeResult(&res, started)
		return res, err
	}

	captureTranscriptRemoved := 0
	for _, rec := range captureTranscriptRows {
		if rec.TranscriptPath == "" {
			continue
		}
		res.Processed++
		err := safeRemoveWithin(rec.TranscriptPath, r.cfg.AudioStoreDir)
		if err != nil && !os.IsNotExist(err) {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("remove capture transcript %s: %v", rec.TranscriptPath, err))
			continue
		}
		if err := r.store.ClearCaptureTranscriptPath(rec.CaptureID); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("clear capture transcript path for %s: %v", rec.CaptureID, err))
			continue
		}
		captureTranscriptRemoved++
		res.Succeeded++
	}

	res.Data["capture_audio_removed"] = captureAudioRemoved
	res.Data["capture_transcript_removed"] = captureTranscriptRemoved
	finalizeResult(&res, started)
	if res.Failed > 0 {
		return res, errors.New("cleanup completed with failures")
	}
	return res, nil
}

func (r *Runner) Status(_ context.Context) (Result, error) {
	started := time.Now()
	res := Result{Command: "status", Data: map[string]any{}}

	summary, err := r.store.Summary(time.Now(), r.cfg.MaxRetryAttempts)
	if err != nil {
		res.Failed = 1
		res.Errors = append(res.Errors, err.Error())
		finalizeResult(&res, started)
		return res, err
	}

	res.Data["summary"] = summary
	finalizeResult(&res, started)
	return res, nil
}

func (r *Runner) processCandidate(ctx context.Context, c Candidate, previousAttempts int) (bool, bool, error) {
	artifacts, err := r.processTarget(ctx, processTarget{
		Source:         "discord",
		CaptureID:      c.Message.ID,
		Kind:           c.Kind,
		TextContent:    c.Message.Content,
		ChannelID:      c.Message.ChannelID,
		MessageID:      c.Message.ID,
		AuthorID:       c.Message.Author.ID,
		GuildID:        c.Message.GuildID,
		JumpURL:        c.JumpURL,
		AttachmentID:   c.Attachment.ID,
		AttachmentURL:  c.Attachment.URL,
		AttachmentName: c.Attachment.Filename,
		ContentType:    c.Attachment.ContentType,
	})
	if err != nil {
		return false, r.scheduleFailure(c.Message.ID, previousAttempts, err), err
	}

	jumpURL := c.JumpURL
	if jumpURL == "" && c.Message.GuildID != "" {
		jumpURL = journal.DiscordJumpURL(c.Message.GuildID, c.Message.ChannelID, c.Message.ID)
	}

	if err := r.discord.AddReaction(ctx, c.Message.ChannelID, c.Message.ID, checkMarkEmojiEscaped); err != nil {
		attempts := previousAttempts + 1
		next := NextRetryAt(time.Now(), attempts, r.cfg.RetryBaseSeconds, r.cfg.RetryMaxSeconds)
		markErr := r.store.MarkReactionPending(
			c.Message.ID,
			err.Error(),
			attempts,
			next,
			artifacts.JournalPath,
			artifacts.RawAudioPath,
			artifacts.TranscriptPath,
			jumpURL,
		)
		if markErr != nil {
			return false, true, fmt.Errorf("reaction failed: %v; and mark reaction_pending failed: %w", err, markErr)
		}
		return false, true, fmt.Errorf("reaction failed: %w", err)
	}

	if err := r.store.MarkDone(c.Message.ID, artifacts.JournalPath, artifacts.RawAudioPath, artifacts.TranscriptPath, jumpURL); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (r *Runner) processStoredCapture(ctx context.Context, rec state.CaptureRecord) (bool, bool, error) {
	kind := kindFromContentType(rec.ContentType)
	if strings.TrimSpace(rec.RawAudioPath) != "" {
		kind = CandidateKindAudio
	}

	artifacts, err := r.processTarget(ctx, processTarget{
		Source:             rec.Source,
		CaptureID:          rec.CaptureID,
		DeviceID:           rec.DeviceID,
		CapturedAt:         rec.CapturedAt,
		PreTranscribedText: rec.TranscriptText,
		Kind:               kind,
		ContentType:        rec.ContentType,
		RawAudioPath:       rec.RawAudioPath,
	})
	if err != nil {
		return false, r.scheduleCaptureFailure(rec.CaptureID, rec.Attempts, err), err
	}
	if err := r.store.MarkCaptureDone(rec.CaptureID, artifacts.JournalPath, artifacts.TranscriptPath); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (r *Runner) processTarget(ctx context.Context, target processTarget) (processArtifacts, error) {
	now := time.Now()
	jumpURL := target.JumpURL
	if jumpURL == "" && target.GuildID != "" && target.ChannelID != "" && target.MessageID != "" {
		jumpURL = journal.DiscordJumpURL(target.GuildID, target.ChannelID, target.MessageID)
	}

	kind := target.Kind
	if kind == "" {
		kind = kindFromContentType(target.ContentType)
		if kind == "" && strings.TrimSpace(target.RawAudioPath) != "" {
			kind = CandidateKindAudio
		}
		if kind == "" && strings.TrimSpace(target.TextContent) != "" {
			kind = CandidateKindText
		}
	}
	if kind == "" {
		return processArtifacts{}, errors.New("unsupported capture kind")
	}

	var transcriptText string
	transcriptPath := ""
	audioPath := target.RawAudioPath
	journalAudioFile := ""
	whisperModel := r.cfg.WhisperModel

	if preTranscribed := strings.TrimSpace(target.PreTranscribedText); preTranscribed != "" {
		transcriptText = preTranscribed
		whisperModel = "gpt-4o-mini-transcribe"
		if strings.TrimSpace(audioPath) != "" {
			journalAudioFile = relativeOrSelf(audioPath, r.cfg.AudioStoreDir)
		} else {
			journalAudioFile = "(text-only)"
		}
	} else if kind == CandidateKindText {
		transcriptText = strings.TrimSpace(target.TextContent)
		whisperModel = "direct-text"
		journalAudioFile = "(text-only)"
	} else {
		origPath := target.RawAudioPath
		if strings.TrimSpace(origPath) == "" {
			subdir := now.Format("2006/01/02")
			prefix := fmt.Sprintf("%s_%s", target.CaptureID, target.AttachmentID)
			origPath = filepath.Join(r.cfg.AudioStoreDir, subdir, prefix+".orig")
			if err := r.discord.DownloadAttachment(ctx, target.AttachmentURL, origPath); err != nil {
				return processArtifacts{}, err
			}
			audioPath = origPath
		}

		baseDir := filepath.Dir(origPath)
		baseName := strings.TrimSuffix(filepath.Base(origPath), filepath.Ext(origPath))
		wavPath := filepath.Join(baseDir, baseName+"_16k.wav")
		transcriptDir := filepath.Join(baseDir, "transcripts")

		if err := transcribe.NormalizeToWav(ctx, r.cfg.FFmpegBin, origPath, wavPath); err != nil {
			return processArtifacts{}, err
		}

		txCtx, cancel := transcribe.ContextWithTranscriptionTimeout(ctx)
		defer cancel()

		txRes, err := transcribe.RunWhisper(
			txCtx,
			transcribe.WhisperConfig{Bin: r.cfg.WhisperBin, Model: r.cfg.WhisperModel, Language: r.cfg.WhisperLanguage},
			wavPath,
			transcriptDir,
		)
		if err != nil {
			return processArtifacts{}, err
		}
		if err := os.Remove(wavPath); err != nil && !os.IsNotExist(err) {
			log.Printf("cleanup normalized wav %s: %v", wavPath, err)
		}
		transcriptText = txRes.Text
		transcriptPath = txRes.TranscriptJSON
		journalAudioFile = relativeOrSelf(origPath, r.cfg.AudioStoreDir)
	}

	journalPath := journal.FilePath(r.cfg.VaultJournalDir, now)
	exists, err := r.obsidian.FileExists(ctx, journalPath)
	if err != nil {
		return processArtifacts{}, err
	}
	if !exists {
		if err := r.obsidian.CreateFile(ctx, journalPath, journal.NewJournalContent(now)); err != nil {
			return processArtifacts{}, err
		}
	}

	entry := journal.BuildEntry(journal.EntryInput{
		Now:          now,
		Transcript:   transcriptText,
		Source:       target.Source,
		CaptureID:    target.CaptureID,
		DeviceID:     target.DeviceID,
		CapturedAt:   target.CapturedAt,
		ChannelID:    target.ChannelID,
		MessageID:    target.MessageID,
		AuthorID:     target.AuthorID,
		JumpURL:      jumpURL,
		AudioFile:    journalAudioFile,
		WhisperModel: whisperModel,
		ProcessedAt:  time.Now(),
	})

	alreadyLogged, err := r.journalContainsCapture(ctx, journalPath, target.Source, target.CaptureID)
	if err != nil {
		return processArtifacts{}, err
	}
	if !alreadyLogged {
		if err := r.obsidian.AppendFile(ctx, journalPath, entry); err != nil {
			return processArtifacts{}, err
		}
	}

	return processArtifacts{
		JournalPath:    journalPath,
		RawAudioPath:   audioPath,
		TranscriptPath: transcriptPath,
	}, nil
}

func (r *Runner) journalContainsCapture(ctx context.Context, journalPath, source, captureID string) (bool, error) {
	content, err := r.obsidian.ReadFile(ctx, journalPath)
	if err != nil {
		return false, err
	}
	marker := fmt.Sprintf("capture_key: \"%s\"", journal.CaptureKey(source, captureID))
	return strings.Contains(content, marker), nil
}

func (r *Runner) scheduleFailure(messageID string, previousAttempts int, processErr error) bool {
	attempts := previousAttempts + 1
	if attempts >= r.cfg.MaxRetryAttempts {
		_ = r.store.MarkFailed(messageID, processErr.Error(), attempts, nil)
		return false
	}
	next := NextRetryAt(time.Now(), attempts, r.cfg.RetryBaseSeconds, r.cfg.RetryMaxSeconds)
	_ = r.store.MarkFailed(messageID, processErr.Error(), attempts, &next)
	return true
}

func (r *Runner) scheduleCaptureFailure(captureID string, previousAttempts int, processErr error) bool {
	attempts := previousAttempts + 1
	if attempts >= r.cfg.MaxRetryAttempts {
		_ = r.store.MarkCaptureFailed(captureID, processErr.Error(), attempts, nil)
		return false
	}
	next := NextRetryAt(time.Now(), attempts, r.cfg.RetryBaseSeconds, r.cfg.RetryMaxSeconds)
	_ = r.store.MarkCaptureFailed(captureID, processErr.Error(), attempts, &next)
	return true
}

func safeRemoveWithin(targetPath, root string) error {
	if strings.TrimSpace(targetPath) == "" {
		return nil
	}
	cleanTarget := filepath.Clean(targetPath)
	cleanRoot := filepath.Clean(root)
	rel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil {
		return err
	}
	if strings.HasPrefix(rel, "..") {
		return fmt.Errorf("path %s is outside root %s", cleanTarget, cleanRoot)
	}
	return os.Remove(cleanTarget)
}

func relativeOrSelf(targetPath, baseDir string) string {
	rel, err := filepath.Rel(baseDir, targetPath)
	if err != nil {
		return targetPath
	}
	if strings.HasPrefix(rel, "..") {
		return targetPath
	}
	return rel
}

func ensureData(data map[string]any) map[string]any {
	if data == nil {
		return map[string]any{}
	}
	return data
}

func kindFromContentType(contentType string) CandidateKind {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "audio/") {
		return CandidateKindAudio
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/") {
		return CandidateKindText
	}
	return ""
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	return nil
}
