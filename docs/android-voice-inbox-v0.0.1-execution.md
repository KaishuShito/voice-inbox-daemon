# Android Voice Inbox v0.0.1 Execution Plan

Date: 2026-03-19
Project: voice-inbox-daemon
Related plan: [android-voice-inbox-plan.md](/Users/kai/Develop/voice-inbox-daemon/docs/android-voice-inbox-plan.md)
Related review: [android-voice-inbox-plan-review.md](/Users/kai/Develop/voice-inbox-daemon/docs/android-voice-inbox-plan-review.md)
Scope: concrete implementation start plan

## Step 0

### What existing code already solves

- [internal/pipeline/runner.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/runner.go) already owns the real value path:
  - normalize
  - transcribe
  - append Journal
  - retry / cleanup behavior
- [internal/state/store.go](/Users/kai/Develop/voice-inbox-daemon/internal/state/store.go) already gives us:
  - SQLite state
  - file lock discipline
  - retry-related persistence patterns
- [internal/journal/journal.go](/Users/kai/Develop/voice-inbox-daemon/internal/journal/journal.go) already defines a stable Journal output shape.

### Minimum diff for v0.0.1

The minimum useful system is:

1. one Mac HTTP ingest endpoint
2. one durable capture row in SQLite before ACK
3. one shared processor path
4. one tiny Android client that uploads audio

Everything else is optional.

### Complexity check

The full system still touches many files, so we should not implement it as one blob.

The safe way is to split into **4 PR-sized slices**:

1. backend durability seam
2. backend processor integration
3. backend operational hardening
4. Android client

That keeps each step testable and reversible.

## Recommended Start Sequence

```text
PR 1: Durable ingest skeleton
  ->
PR 2: Shared processor for capture rows
  ->
PR 3: Recovery / retry / cleanup correctness
  ->
PR 4: Android capture client
```

Do not start with Android first.

The Android client needs a stable server contract, and the plan review already showed that the risky part is backend durability, not UI.

## Execution Slices

## PR 1: Durable Ingest Skeleton

Goal:

- accept upload
- durably store file + DB row
- return success only after durable registration

### Files to touch

- [cmd/voice-inbox/main.go](/Users/kai/Develop/voice-inbox-daemon/cmd/voice-inbox/main.go)
- [internal/config/config.go](/Users/kai/Develop/voice-inbox-daemon/internal/config/config.go)
- [internal/state/store.go](/Users/kai/Develop/voice-inbox-daemon/internal/state/store.go)
- [internal/ingest/server.go](/Users/kai/Develop/voice-inbox-daemon/internal/ingest/server.go)
- [internal/ingest/server_test.go](/Users/kai/Develop/voice-inbox-daemon/internal/ingest/server_test.go)

### Deliverables

- `voice-inbox serve`
- `POST /v0/captures`
- bearer token auth
- raw audio saved under managed storage
- `captures` table created with additive migration
- idempotent duplicate handling by `capture_id` or `source_dedupe_key`

### Acceptance criteria

- upload returns success only after file + row both exist
- duplicate request does not create duplicate rows
- malformed multipart fails cleanly
- unauthorized request fails cleanly

### Stop condition

Do not add processing in this PR.
This PR should prove durable registration only.

## PR 2: Shared Processor Integration

Goal:

- a capture row can be processed through the same core path that Discord uses today

### Files to touch

- [internal/pipeline/runner.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/runner.go)
- [internal/journal/journal.go](/Users/kai/Develop/voice-inbox-daemon/internal/journal/journal.go)
- [internal/journal/journal_test.go](/Users/kai/Develop/voice-inbox-daemon/internal/journal/journal_test.go)
- [internal/pipeline/runner_integration_test.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/runner_integration_test.go)

### Deliverables

- shared helper for source-neutral processing
- HTTP-origin capture metadata in Journal entries
- no HTTP-specific journal path outside the shared processor
- Discord reaction logic remains Discord-only

### Acceptance criteria

- one persisted capture row becomes one Journal append
- same row does not append twice
- Discord path still works unchanged

### Stop condition

Do not add cleanup policy changes beyond what is required for correctness.

## PR 3: Recovery, Retry, and Cleanup Correctness

Goal:

- crashes and retries do not lose or duplicate captures

### Files to touch

- [internal/state/store.go](/Users/kai/Develop/voice-inbox-daemon/internal/state/store.go)
- [internal/pipeline/retry.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/retry.go)
- [internal/pipeline/runner.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/runner.go)
- [internal/pipeline/retry_test.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/retry_test.go)
- [docs/OPERATIONS.md](/Users/kai/Develop/voice-inbox-daemon/docs/OPERATIONS.md)

### Deliverables

- stale `processing` capture recovery on restart
- retry selection for HTTP captures
- cleanup that never deletes unfinished capture files
- operator-visible status for capture rows

### Acceptance criteria

- daemon restart after ACK does not lose a capture
- failed Obsidian/transcription work requeues correctly
- cleanup ignores anything not `done`

### Stop condition

Do not add fancy dashboards or observability systems.
Plain status output and log visibility are enough for `v0.0.1`.

## PR 4: Android Capture Client

Goal:

- one-tap-ish capture and upload from Pixel 8a

### Android build target

Create a new Android app repo or app project separately.

For `v0.0.1`, the app should include:

- Quick Settings tile
- immediate record flow
- stop button
- upload state
- short sent state
- local retry for failed uploads

### Required screens/states

- `Recording`
- `Sending`
- `Sent`
- `Error`

### Acceptance criteria

- user can capture and send without typing
- no transcript preview exists in happy path
- failed upload is recoverable

### Stop condition

Do not add:

- transcript view
- note list
- settings screen beyond server URL/token if absolutely needed
- multi-destination support

## Concrete Order of Work

### Day 1

- define `captures` table shape
- add ingest config
- add `serve` command skeleton
- implement auth + multipart parsing

### Day 2

- persist raw file and capture row before ACK
- add ingest tests
- define processor handoff contract

### Day 3

- refactor shared processing helper
- run capture row through normalization/transcription/journal
- extend Journal metadata

### Day 4

- add restart recovery
- add retry integration
- add cleanup protections

### Day 5

- manual end-to-end with `curl`
- update operations doc
- lock server contract for Android

Only after Day 5 should Android implementation begin.

## Server Contract to Freeze Before Android

Android should not start against a moving target.

Freeze these before mobile work:

- endpoint path
- auth header format
- multipart field names
- response body shape
- max upload size
- duplicate behavior
- retry-safe `capture_id` semantics

## Test Plan

```text
            +---------------------------+
            | PR 1 ingest handler tests |
            | auth/body/idempotency     |
            +-------------+-------------+
                          |
                          v
         +---------------------------------------+
         | PR 2 shared processor integration     |
         | capture row -> journal append         |
         +------------------+--------------------+
                            |
                            v
         +---------------------------------------+
         | PR 3 restart / retry / cleanup tests  |
         | recovery and duplicate suppression    |
         +------------------+--------------------+
                            |
                            v
         +---------------------------------------+
         | PR 4 Android manual E2E validation    |
         | tile -> record -> upload -> sent      |
         +---------------------------------------+
```

### Required test list

#### PR 1

- unauthorized upload
- malformed multipart
- oversized body
- duplicate upload same key
- success only after durable registration

#### PR 2

- capture row processes once
- Journal metadata correct for HTTP source
- Discord path regression coverage

#### PR 3

- stale `processing` row recovery
- retry after Obsidian failure
- cleanup does not remove unfinished capture artifacts
- no duplicate append after retry

#### PR 4

- manual Pixel test on same network or Tailscale
- app resume/retry behavior after network interruption

## Risks to Watch From Minute One

### 1. Wrong abstraction boundary

If implementation starts introducing a second parallel pipeline, stop and collapse back to one shared processor.

### 2. Premature Android complexity

If mobile work starts asking for transcript display or note management, stop and cut scope.

### 3. ACK too early

If the server can return success before durable registration, treat that as a release blocker.

### 4. Locking inconsistency

If the new capture processor bypasses the repo's current lock discipline, treat that as a release blocker.

## Definition of Ready

Implementation can begin when all of these are true:

- `captures` table shape is agreed
- HTTP endpoint contract is agreed
- durable-before-ACK rule is agreed
- one shared processor ownership rule is agreed
- Android is explicitly out of scope until backend contract is frozen

## Opinionated Recommendation

Start with **PR 1 backend durability seam** immediately.

That is the highest-risk, most reusable, most boring-by-default slice.
If PR 1 is right, the rest gets easier.
If PR 1 is wrong, Android work will amplify the mistake.

So the concrete starting move is:

`implement serve + captures table + durable ingest tests before anything else`
