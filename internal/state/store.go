package state

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type MessageRecord struct {
	MessageID          string
	ChannelID          string
	AuthorID           string
	AttachmentID       string
	AttachmentURL      string
	AttachmentFilename string
	ContentType        string
	AudioPath          string
	TranscriptPath     string
	Status             string
	Attempts           int
	NextRetryAt        *time.Time
	LastError          string
	JournalPath        string
	DiscordJumpURL     string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type StatusSummary struct {
	Total              int            `json:"total"`
	ByStatus           map[string]int `json:"by_status"`
	RetryDue           int            `json:"retry_due"`
	PermanentFailCount int            `json:"permanent_failed"`
	LastSeenMessageID  string         `json:"last_seen_message_id,omitempty"`
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS messages (
		  message_id TEXT PRIMARY KEY,
		  channel_id TEXT NOT NULL,
		  author_id TEXT NOT NULL,
		  attachment_id TEXT NOT NULL,
		  attachment_url TEXT NOT NULL,
		  attachment_filename TEXT,
		  content_type TEXT,
		  audio_path TEXT,
		  transcript_path TEXT,
		  status TEXT NOT NULL,
		  attempts INTEGER NOT NULL DEFAULT 0,
		  next_retry_at TEXT,
		  last_error TEXT,
		  journal_path TEXT,
		  discord_jump_url TEXT,
		  created_at TEXT NOT NULL,
		  updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS runs (
		  run_id TEXT PRIMARY KEY,
		  command TEXT NOT NULL,
		  started_at TEXT NOT NULL,
		  finished_at TEXT,
		  processed_count INTEGER NOT NULL DEFAULT 0,
		  success_count INTEGER NOT NULL DEFAULT 0,
		  failed_count INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS kv (
		  key TEXT PRIMARY KEY,
		  value TEXT NOT NULL,
		  updated_at TEXT NOT NULL
		);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return s.SetKV("schema_version", "1")
}

func (s *Store) BeginRun(command string, startedAt time.Time) (string, error) {
	runID, err := randomID()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO runs (run_id, command, started_at) VALUES (?, ?, ?)`, runID, command, startedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	return runID, nil
}

func (s *Store) FinishRun(runID string, finishedAt time.Time, processed, success, failed int) error {
	_, err := s.db.Exec(`
		UPDATE runs
		SET finished_at = ?, processed_count = ?, success_count = ?, failed_count = ?
		WHERE run_id = ?`,
		finishedAt.UTC().Format(time.RFC3339), processed, success, failed, runID)
	return err
}

func (s *Store) SetKV(key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
		INSERT INTO kv (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, key, value, now)
	return err
}

func (s *Store) GetKV(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (s *Store) UpsertPending(rec MessageRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
		INSERT INTO messages (
			message_id, channel_id, author_id, attachment_id, attachment_url,
			attachment_filename, content_type, status, attempts, created_at, updated_at,
			discord_jump_url
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?)
		ON CONFLICT(message_id) DO UPDATE SET
			channel_id = excluded.channel_id,
			author_id = excluded.author_id,
			attachment_id = excluded.attachment_id,
			attachment_url = excluded.attachment_url,
			attachment_filename = excluded.attachment_filename,
			content_type = excluded.content_type,
			discord_jump_url = excluded.discord_jump_url,
			updated_at = excluded.updated_at
	`,
		rec.MessageID, rec.ChannelID, rec.AuthorID, rec.AttachmentID, rec.AttachmentURL,
		rec.AttachmentFilename, rec.ContentType, now, now, rec.DiscordJumpURL,
	)
	return err
}

func (s *Store) GetMessage(messageID string) (MessageRecord, bool, error) {
	row := s.db.QueryRow(`
		SELECT message_id, channel_id, author_id, attachment_id, attachment_url, attachment_filename,
			content_type, audio_path, transcript_path, status, attempts, next_retry_at,
			last_error, journal_path, discord_jump_url, created_at, updated_at
		FROM messages WHERE message_id = ?
	`, messageID)

	rec, found, err := scanMessageRow(row)
	if err == sql.ErrNoRows {
		return MessageRecord{}, false, nil
	}
	if err != nil {
		return MessageRecord{}, false, err
	}
	return rec, found, nil
}

func (s *Store) MarkDone(messageID, journalPath, audioPath, transcriptPath, jumpURL string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
		UPDATE messages SET
			status = 'done',
			journal_path = ?,
			audio_path = ?,
			transcript_path = ?,
			discord_jump_url = ?,
			last_error = NULL,
			next_retry_at = NULL,
			updated_at = ?
		WHERE message_id = ?
	`, journalPath, nullable(audioPath), nullable(transcriptPath), nullable(jumpURL), now, messageID)
	return err
}

func (s *Store) MarkReactionPending(
	messageID,
	errText string,
	attempts int,
	nextRetryAt time.Time,
	journalPath,
	audioPath,
	transcriptPath,
	jumpURL string,
) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
		UPDATE messages SET
			status = 'reaction_pending',
			attempts = ?,
			journal_path = ?,
			audio_path = ?,
			transcript_path = ?,
			discord_jump_url = ?,
			last_error = ?,
			next_retry_at = ?,
			updated_at = ?
		WHERE message_id = ?
	`,
		attempts,
		nullable(journalPath),
		nullable(audioPath),
		nullable(transcriptPath),
		nullable(jumpURL),
		trimError(errText),
		nextRetryAt.UTC().Format(time.RFC3339),
		now,
		messageID,
	)
	return err
}

func (s *Store) MarkFailed(messageID, errText string, attempts int, nextRetryAt *time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var next any
	if nextRetryAt != nil {
		next = nextRetryAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.Exec(`
		UPDATE messages SET
			status = 'failed',
			attempts = ?,
			last_error = ?,
			next_retry_at = ?,
			updated_at = ?
		WHERE message_id = ?
	`, attempts, trimError(errText), next, now, messageID)
	return err
}

func (s *Store) ListRetryCandidates(now time.Time, limit int) ([]MessageRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT message_id, channel_id, author_id, attachment_id, attachment_url, attachment_filename,
			content_type, audio_path, transcript_path, status, attempts, next_retry_at,
			last_error, journal_path, discord_jump_url, created_at, updated_at
		FROM messages
		WHERE status IN ('failed', 'reaction_pending')
		  AND (
		        (status = 'reaction_pending' AND (next_retry_at IS NULL OR next_retry_at <= ?))
		        OR
		        (status = 'failed' AND next_retry_at IS NOT NULL AND next_retry_at <= ?)
		      )
		ORDER BY updated_at ASC
		LIMIT ?
	`, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MessageRecord
	for rows.Next() {
		rec, found, err := scanMessageRows(rows)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, rec)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ListDoneWithAudioBefore(cutoff time.Time, limit int) ([]MessageRecord, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`
		SELECT message_id, channel_id, author_id, attachment_id, attachment_url, attachment_filename,
			content_type, audio_path, transcript_path, status, attempts, next_retry_at,
			last_error, journal_path, discord_jump_url, created_at, updated_at
		FROM messages
		WHERE status = 'done'
		  AND audio_path IS NOT NULL
		  AND updated_at < ?
		ORDER BY updated_at ASC
		LIMIT ?
	`, cutoff.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MessageRecord
	for rows.Next() {
		rec, found, err := scanMessageRows(rows)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, rec)
		}
	}
	return out, rows.Err()
}

func (s *Store) ListDoneWithTranscriptBefore(cutoff time.Time, limit int) ([]MessageRecord, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`
		SELECT message_id, channel_id, author_id, attachment_id, attachment_url, attachment_filename,
			content_type, audio_path, transcript_path, status, attempts, next_retry_at,
			last_error, journal_path, discord_jump_url, created_at, updated_at
		FROM messages
		WHERE status = 'done'
		  AND transcript_path IS NOT NULL
		  AND updated_at < ?
		ORDER BY updated_at ASC
		LIMIT ?
	`, cutoff.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MessageRecord
	for rows.Next() {
		rec, found, err := scanMessageRows(rows)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, rec)
		}
	}
	return out, rows.Err()
}

func (s *Store) ClearAudioPath(messageID string) error {
	_, err := s.db.Exec(`UPDATE messages SET audio_path = NULL, updated_at = ? WHERE message_id = ?`, time.Now().UTC().Format(time.RFC3339), messageID)
	return err
}

func (s *Store) ClearTranscriptPath(messageID string) error {
	_, err := s.db.Exec(`UPDATE messages SET transcript_path = NULL, updated_at = ? WHERE message_id = ?`, time.Now().UTC().Format(time.RFC3339), messageID)
	return err
}

func (s *Store) Summary(now time.Time, maxRetryAttempts int) (StatusSummary, error) {
	summary := StatusSummary{ByStatus: map[string]int{}}

	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM messages GROUP BY status`)
	if err != nil {
		return summary, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return summary, err
		}
		summary.ByStatus[status] = count
		summary.Total += count
	}
	if err := rows.Err(); err != nil {
		return summary, err
	}

	_ = s.db.QueryRow(`
		SELECT COUNT(*)
		FROM messages
		WHERE status IN ('failed', 'reaction_pending')
		  AND (
		        (status = 'reaction_pending' AND (next_retry_at IS NULL OR next_retry_at <= ?))
		        OR
		        (status = 'failed' AND next_retry_at IS NOT NULL AND next_retry_at <= ?)
		      )
	`, now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)).Scan(&summary.RetryDue)

	_ = s.db.QueryRow(`
		SELECT COUNT(*)
		FROM messages
		WHERE status = 'failed' AND attempts >= ?
	`, maxRetryAttempts).Scan(&summary.PermanentFailCount)

	if v, ok, err := s.GetKV("last_seen_message_id"); err == nil && ok {
		summary.LastSeenMessageID = v
	}
	return summary, nil
}

func scanMessageRows(rows *sql.Rows) (MessageRecord, bool, error) {
	var rec MessageRecord
	var nextRetry sql.NullString
	var audioPath sql.NullString
	var transcriptPath sql.NullString
	var lastError sql.NullString
	var journalPath sql.NullString
	var jumpURL sql.NullString
	var contentType sql.NullString
	var attachmentFilename sql.NullString
	var createdAtRaw string
	var updatedAtRaw string

	err := rows.Scan(
		&rec.MessageID,
		&rec.ChannelID,
		&rec.AuthorID,
		&rec.AttachmentID,
		&rec.AttachmentURL,
		&attachmentFilename,
		&contentType,
		&audioPath,
		&transcriptPath,
		&rec.Status,
		&rec.Attempts,
		&nextRetry,
		&lastError,
		&journalPath,
		&jumpURL,
		&createdAtRaw,
		&updatedAtRaw,
	)
	if err != nil {
		return MessageRecord{}, false, err
	}
	if attachmentFilename.Valid {
		rec.AttachmentFilename = attachmentFilename.String
	}
	if contentType.Valid {
		rec.ContentType = contentType.String
	}
	if audioPath.Valid {
		rec.AudioPath = audioPath.String
	}
	if transcriptPath.Valid {
		rec.TranscriptPath = transcriptPath.String
	}
	if lastError.Valid {
		rec.LastError = lastError.String
	}
	if journalPath.Valid {
		rec.JournalPath = journalPath.String
	}
	if jumpURL.Valid {
		rec.DiscordJumpURL = jumpURL.String
	}
	if nextRetry.Valid {
		t, err := time.Parse(time.RFC3339, nextRetry.String)
		if err == nil {
			rec.NextRetryAt = &t
		}
	}
	if t, err := time.Parse(time.RFC3339, createdAtRaw); err == nil {
		rec.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, updatedAtRaw); err == nil {
		rec.UpdatedAt = t
	}
	return rec, true, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessageRow(row rowScanner) (MessageRecord, bool, error) {
	var rec MessageRecord
	var nextRetry sql.NullString
	var audioPath sql.NullString
	var transcriptPath sql.NullString
	var lastError sql.NullString
	var journalPath sql.NullString
	var jumpURL sql.NullString
	var contentType sql.NullString
	var attachmentFilename sql.NullString
	var createdAtRaw string
	var updatedAtRaw string

	err := row.Scan(
		&rec.MessageID,
		&rec.ChannelID,
		&rec.AuthorID,
		&rec.AttachmentID,
		&rec.AttachmentURL,
		&attachmentFilename,
		&contentType,
		&audioPath,
		&transcriptPath,
		&rec.Status,
		&rec.Attempts,
		&nextRetry,
		&lastError,
		&journalPath,
		&jumpURL,
		&createdAtRaw,
		&updatedAtRaw,
	)
	if err != nil {
		return MessageRecord{}, false, err
	}
	if attachmentFilename.Valid {
		rec.AttachmentFilename = attachmentFilename.String
	}
	if contentType.Valid {
		rec.ContentType = contentType.String
	}
	if audioPath.Valid {
		rec.AudioPath = audioPath.String
	}
	if transcriptPath.Valid {
		rec.TranscriptPath = transcriptPath.String
	}
	if lastError.Valid {
		rec.LastError = lastError.String
	}
	if journalPath.Valid {
		rec.JournalPath = journalPath.String
	}
	if jumpURL.Valid {
		rec.DiscordJumpURL = jumpURL.String
	}
	if nextRetry.Valid {
		t, err := time.Parse(time.RFC3339, nextRetry.String)
		if err == nil {
			rec.NextRetryAt = &t
		}
	}
	if t, err := time.Parse(time.RFC3339, createdAtRaw); err == nil {
		rec.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, updatedAtRaw); err == nil {
		rec.UpdatedAt = t
	}
	return rec, true, nil
}

func randomID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func trimError(errText string) string {
	errText = strings.TrimSpace(errText)
	if len(errText) > 1000 {
		return errText[:1000]
	}
	return errText
}
