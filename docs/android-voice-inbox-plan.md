# Android Voice Inbox Plan v2

Date: 2026-03-19
Project: voice-inbox-daemon
Target release: v0.0.1
Status: implementation plan only

## 0. Step 0 Scope Challenge

The product is a **cognitive-cost-zero data generation device**.

It is not:

- a general memo app
- a transcript editor
- a note browser
- a mobile knowledge management client

For `v0.0.1`, success is:

- capture frequency
- daily usage
- time-to-record
- time-to-send
- recovery from ordinary failure without manual debugging

For `v0.0.1`, success is not:

- transcript polish
- transcript editing
- categories/tags/titles
- perfect lock-screen integration
- cloud sync for every network condition

### What existing code already solves

The current repo already solves the expensive back half:

- raw audio normalization via `ffmpeg`
- transcription via `whisper`
- Journal append via Obsidian Local REST API
- SQLite-backed state and retries
- cleanup operations
- durable local storage for audio artifacts

Relevant existing files:

- [cmd/voice-inbox/main.go](/Users/kai/Develop/voice-inbox-daemon/cmd/voice-inbox/main.go)
- [internal/config/config.go](/Users/kai/Develop/voice-inbox-daemon/internal/config/config.go)
- [internal/pipeline/runner.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/runner.go)
- [internal/state/store.go](/Users/kai/Develop/voice-inbox-daemon/internal/state/store.go)
- [internal/journal/journal.go](/Users/kai/Develop/voice-inbox-daemon/internal/journal/journal.go)

### Minimum diff assessment

The minimum correct diff is:

1. Add one new durable HTTP ingest path on Mac
2. Persist upload metadata to SQLite before returning success
3. Reuse the existing processing path as much as possible
4. Build a tiny Android capture client against that endpoint

The minimum correct diff is not:

- a new backend service
- a second processing pipeline
- a mobile transcript UX
- a daemon rewrite around a web framework

### Why not overbuild

The biggest design risk is mixing **capture** with **synthesis**.

For `v0.0.1`, the backend should remain:

```text
ingest -> durably register capture -> process -> append Journal -> retry/cleanup
```

The Android app should remain:

```text
launch -> record -> stop -> upload -> sent
```

Anything beyond that should be deferred.

## 1. Recommended Architecture

### Product flow

```text
[Pixel 8a]
  quick tile / shortcut / lock-screen-adjacent launch
            |
            v
[Tiny Android app]
  start recording immediately
  stop recording
  auto-upload raw audio
  show short "sent" state
            |
            v
[voice-inbox-daemon serve]
  authenticate request
  save raw file
  insert durable capture row in SQLite
  return 202/200 only after both succeed
            |
            v
[shared processor]
  normalize -> transcribe -> append Journal
            |
            +--> retry / recovery / cleanup
            |
            v
[Obsidian Journal]
  /Users/kai/Documents/Obsidian/01_Projects/Journal
```

### Backend processing model

```text
                         +----------------------+
Discord poll ----------->| existing source path |
                         +----------+-----------+
                                    |
HTTP upload ------------> durable capture record + raw audio file
                                    |
                                    v
                         +----------------------+
                         | shared processor     |
                         | normalize/transcribe |
                         | journal append       |
                         +----------+-----------+
                                    |
                                    v
                         retry / cleanup / status
```

### Opinionated architecture choice

Use one Mac-side binary.

Add a `serve` command that accepts uploads and writes a durable capture record.
Do not create a separate API service.
Do not create a second journal-writing path.
Do not create an in-memory queue that can lose work on restart.

## 2. Exact Backend Changes by File Path

### [cmd/voice-inbox/main.go](/Users/kai/Develop/voice-inbox-daemon/cmd/voice-inbox/main.go)

Add a new command:

- `voice-inbox serve`

Responsibilities:

- load existing config
- open existing SQLite store
- start the HTTP ingest server
- expose graceful shutdown
- optionally run the processing loop in the same process

Keep existing commands unchanged:

- `doctor`
- `poll --once`
- `retry`
- `cleanup`
- `status`

### [internal/config/config.go](/Users/kai/Develop/voice-inbox-daemon/internal/config/config.go)

Add only the config needed for `v0.0.1`:

- `INGEST_LISTEN_ADDR`
- `INGEST_AUTH_TOKEN`
- `INGEST_MAX_BODY_MB`
- `INGEST_SOURCE_NAME` default `android-voice-inbox`
- `INGEST_ALLOWED_CLOCK_SKEW_SECONDS` only if needed for timestamp validation

Do not add:

- multi-user auth config
- complex ACLs
- cloud provider settings

### [internal/state/store.go](/Users/kai/Develop/voice-inbox-daemon/internal/state/store.go)

This is the most important change.

Add a new durable capture model for non-Discord ingest.

Recommended shape:

- new table `captures`
- additive migration only

Suggested columns:

- `capture_id TEXT PRIMARY KEY`
- `source TEXT NOT NULL`
- `source_dedupe_key TEXT`
- `device_id TEXT`
- `received_at TEXT NOT NULL`
- `raw_audio_path TEXT NOT NULL`
- `content_type TEXT`
- `status TEXT NOT NULL`
- `attempts INTEGER NOT NULL DEFAULT 0`
- `next_retry_at TEXT`
- `journal_path TEXT`
- `transcript_path TEXT`
- `error TEXT`
- `created_at TEXT NOT NULL`
- `updated_at TEXT NOT NULL`

Required statuses:

- `received`
- `processing`
- `done`
- `failed`

Required behavior:

- insert capture row before ACK
- support idempotent duplicate upload handling through `source_dedupe_key`
- expose fetch-next-work helpers for the processor
- expose retry selection helpers

Do not try to force HTTP uploads into the existing Discord `messages` identity model.

### [internal/pipeline/runner.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/runner.go)

Do not build a second pipeline.

Instead:

- extract the common processing logic into a helper that can operate on:
  - a Discord-derived job
  - a capture-row-derived job
- keep Discord-specific reaction logic on the Discord side only
- keep Journal entry generation shared
- keep transcription shared

Important sequencing rule:

- HTTP ingest should not directly append to Journal inside the request handler
- request handler should persist and enqueue only
- processing loop should own normalize/transcribe/journal append

### [internal/journal/journal.go](/Users/kai/Develop/voice-inbox-daemon/internal/journal/journal.go)

Extend metadata format to support HTTP-origin captures while preserving current Discord compatibility.

Recommended:

- keep existing Discord fields when source is Discord
- add a neutral origin block for HTTP captures
- keep markdown structure stable

Example metadata intent:

```yaml
voice_inbox:
  source: "android-http"
  capture_id: "..."
  device_id: "pixel8a"
  raw_audio_file: "..."
  whisper_model: "..."
  processed_at: "..."
```

### New file: `/Users/kai/Develop/voice-inbox-daemon/internal/ingest/server.go`

Responsibilities:

- `POST /v0/captures`
- bearer-token auth
- multipart upload parsing
- body size guard
- raw audio file persistence
- durable capture row insertion
- idempotent duplicate handling
- fast success response after durable write

Keep this package narrow.

### New file: `/Users/kai/Develop/voice-inbox-daemon/internal/ingest/server_test.go`

Cover:

- unauthorized request
- oversize body
- malformed multipart
- happy path
- duplicate upload by same dedupe key

### Existing files to extend

- [internal/pipeline/runner_integration_test.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/runner_integration_test.go)
- [internal/journal/journal_test.go](/Users/kai/Develop/voice-inbox-daemon/internal/journal/journal_test.go)
- [internal/obsidian/client_test.go](/Users/kai/Develop/voice-inbox-daemon/internal/obsidian/client_test.go)

## 3. Concurrency and Durability Model

This is the key correction in `v2`.

### Rule 1: No success before durable registration

The HTTP handler must not respond `200/202` until both are true:

1. raw file is written to disk
2. capture row is committed to SQLite

If either step fails, the request fails.

### Rule 2: One processor owns journal append

Do not let:

- HTTP handler
- poller
- retry command

each append to Journal independently.

One shared processing path should own:

- normalization
- transcription
- Journal append
- transition to `done`

### Rule 3: Reuse the existing lock discipline

The plan review correctly flagged coordination risk.

For `v0.0.1`, the simplest safe approach is:

- keep using the existing file lock for state-mutating processing commands
- ensure the `serve` path only does:
  - file write
  - DB insert/update
- have the actual processing loop acquire the same lock discipline used by existing processing commands

That gives boring, understandable serialization before any future optimization.

### Rule 4: Restart recovery is mandatory

On process startup:

- any `processing` capture older than a safe timeout should be moved back to `received` or retryable state
- any `received` capture with raw file present should be processable

This is what makes `v0.0.1` trustworthy.

## 4. Network and Auth Strategy

### Recommendation for v0.0.1

Use:

- **Tailscale**
- **single bearer token**

Why:

- works when the phone is not on home Wi-Fi
- avoids opening the daemon to the public internet
- low operational complexity for one user
- good enough security for a personal system

### Do not do in v0.0.1

- public HTTPS exposure
- OAuth
- multi-device account system
- complicated cert automation

### Endpoint contract

Recommended initial contract:

- `POST /v0/captures`
- `Authorization: Bearer <token>`
- multipart fields:
  - `audio`
  - `capture_id`
  - `device_id`
  - `captured_at`
  - optional `content_type`

Response:

- `202 Accepted` with JSON body containing:
  - `capture_id`
  - `status`
  - `received_at`

## 5. Android App v0.0.1 Scope

For `v0.0.1`, the Android app should stay tiny.

### Recommended app structure

One app module is enough.

Internal packages can still be lightweight:

- `capture`
- `upload`
- `ui`

Do not overdesign module boundaries yet.

### Screens and states

#### `RecordEntry`

- launched from quick settings tile or launcher shortcut
- immediately transitions into recording

#### `Recording`

- elapsed timer
- stop button
- optional cancel button only if technically necessary

#### `Sending`

- blocking spinner while upload is in progress

#### `Sent`

- short confirmation only
- auto-dismiss

#### `Error`

- shown only when upload fails
- offer `retry now`
- otherwise keep capture locally for background retry

### Launch recommendation

For `v0.0.1`, prioritize:

1. Quick Settings tile
2. launcher shortcut

Treat true lock-screen direct launch as an optimization to investigate later.
Pixel 8a may allow useful lock-screen-adjacent flows, but the plan should not depend on them.

### Recording behavior

- app opens and starts recording fast
- stop triggers immediate upload
- no title field
- no transcript preview
- no destination choice
- no category/tag UI

## 6. Failure Modes and Retry Strategy

### Core failure cases

1. Android records, upload fails
2. Android upload succeeds, daemon crashes before processing
3. Daemon processes, Obsidian append fails
4. Duplicate upload from retry or double tap
5. Raw file exists but DB row is missing
6. DB row exists but raw file is missing

### v0.0.1 strategy

#### On Android

- keep a local pending upload entry until server acknowledges durable receipt
- background retry with simple exponential backoff
- one visible error state only when immediate upload fails

#### On Mac

- use `captures.status` plus `attempts` and `next_retry_at`
- cleanup job should never delete files tied to non-`done` captures
- orphan reconciliation should be explicit:
  - missing DB row for file -> log and quarantine
  - missing file for row -> mark failed

### Idempotency rule

Android must generate a stable `capture_id` / dedupe key per recording.

That same key must be reused for retries so the server can return the already-known row instead of creating duplicates.

## 7. Phased Implementation Plan

## Phase 1: Backend durability seam

Goal:

- daemon can durably accept HTTP uploads and recover after restart

Deliverables:

- `serve` command
- ingest server
- `captures` table
- shared processor entry for capture rows

Acceptance criteria:

- uploading one audio file creates a raw file and DB row before ACK
- restarting the daemon after ACK does not lose the capture
- processing eventually appends to Journal
- duplicate upload with same dedupe key does not create duplicate Journal entries

## Phase 2: Minimal Android client

Goal:

- one-tap-ish recording and auto-upload from Pixel 8a

Deliverables:

- tiny Compose app
- quick settings tile
- record/stop/upload/sent flow
- local retry queue

Acceptance criteria:

- from tile tap to recording start feels immediate
- stop leads to upload with no confirmation screen
- success indicator is brief and clear
- failed upload is recoverable

## Phase 3: Habit validation

Goal:

- prove real usage before broadening scope

Deliverables:

- capture metrics logging
- small operational notes
- lock-screen-adjacent launch experiments

Acceptance criteria:

- used on real walks/commutes/transition moments
- repeated daily without annoyance
- no pressure to manually clean broken state

## 8. Test Plan

### Test diagram

```text
          +------------------------+
          | HTTP ingest tests      |
          | auth/body/idempotency  |
          +-----------+------------+
                      |
                      v
          +------------------------+
          | durable state tests    |
          | insert/recover/retry   |
          +-----------+------------+
                      |
                      v
          +------------------------+
          | processor integration  |
          | normalize -> journal   |
          +-----------+------------+
                      |
         +------------+-------------+
         |                          |
         v                          v
  restart recovery             duplicate suppression
```

### Concrete test layers

#### Layer 1: ingest handler

- auth required
- body size enforced
- malformed multipart rejected
- supported audio content types accepted
- duplicate dedupe key returns stable result

#### Layer 2: store / migration

- old Discord-backed rows still load
- new `captures` rows migrate cleanly
- status transitions are valid
- stale `processing` rows recover on restart

#### Layer 3: processor integration

- capture row becomes Journal append
- Obsidian failure schedules retry
- missing raw file marks failure
- duplicate capture does not duplicate append

#### Layer 4: command/process coordination

- `serve` + `retry` do not corrupt state
- file lock discipline serializes processing work
- cleanup ignores unfinished captures

## 9. Open Questions

Keep these few and high-signal.

1. On Pixel 8a, is Quick Settings tile sufficient, or is there a reliable lock-screen shortcut path worth supporting in `v0.0.1`?
2. Should the server return `200 OK` or `202 Accepted` after durable registration but before processing completes? Recommendation: `202 Accepted`.
3. Should raw uploads be stored in their original codec only, or should the server also preserve a normalized WAV artifact? Recommendation: keep original + derived WAV as today.

## 10. Opinionated Recommendation Summary

Build `v0.0.1` as a narrow, durable system:

- tiny Android capture app
- Quick Settings tile first
- no confirmation UI
- raw audio upload only
- single Mac binary
- HTTP ingest that durably writes file + SQLite row before ACK
- one shared processor path that owns Journal append

Do not expand into transcript UX, note management, or generalized mobile knowledge tooling until the capture habit is real.

If we stay disciplined, `v0.0.1` can prove the only thing that matters:

`Does this actually increase real-world capture moments?`
