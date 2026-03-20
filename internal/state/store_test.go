package state

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenMigratesCaptureSchemaV2ToV3Idempotently(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	stmts := []string{
		`CREATE TABLE kv (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE messages (
			message_id TEXT PRIMARY KEY,
			channel_id TEXT NOT NULL,
			author_id TEXT NOT NULL,
			attachment_id TEXT NOT NULL,
			attachment_url TEXT NOT NULL,
			attachment_filename TEXT,
			content_type TEXT,
			message_content TEXT,
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
		`CREATE TABLE runs (
			run_id TEXT PRIMARY KEY,
			command TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			processed_count INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			failed_count INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE captures (
			capture_id TEXT PRIMARY KEY,
			source TEXT NOT NULL,
			source_dedupe_key TEXT,
			device_id TEXT,
			captured_at TEXT,
			received_at TEXT NOT NULL,
			raw_audio_path TEXT NOT NULL,
			content_type TEXT,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_retry_at TEXT,
			journal_path TEXT,
			transcript_path TEXT,
			last_error TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`INSERT INTO kv (key, value, updated_at) VALUES ('schema_version', '2', '2026-03-20T00:00:00Z');`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	version, ok, err := st.GetKV("schema_version")
	if err != nil {
		t.Fatalf("get schema version: %v", err)
	}
	if !ok || version != "3" {
		t.Fatalf("expected schema_version=3, got ok=%v value=%q", ok, version)
	}
	assertCaptureColumnExists(t, st.db, "transcript_text")
	if err := st.Close(); err != nil {
		t.Fatalf("close migrated store: %v", err)
	}

	st, err = Open(dbPath)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer func() { _ = st.Close() }()

	version, ok, err = st.GetKV("schema_version")
	if err != nil {
		t.Fatalf("get schema version after reopen: %v", err)
	}
	if !ok || version != "3" {
		t.Fatalf("expected schema_version=3 after reopen, got ok=%v value=%q", ok, version)
	}
	assertCaptureColumnExists(t, st.db, "transcript_text")
}

func assertCaptureColumnExists(t *testing.T, db *sql.DB, column string) {
	t.Helper()

	rows, err := db.Query(`PRAGMA table_info(captures)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var dataType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == column {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info: %v", err)
	}
	t.Fatalf("expected captures.%s column to exist", column)
}
