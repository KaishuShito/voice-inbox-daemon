# Android Voice Inbox Plan Review

## 1. Step 0 Scope Challenge

- Existing code already solves the expensive back half. `internal/pipeline/runner.go` already does normalize -> transcribe -> append to Journal -> retry/cleanup, `internal/state/store.go` already owns durable state and retry bookkeeping, and `internal/journal/journal.go` already defines the stable journal append format.
- The minimum diff is not "new backend + new Android app architecture." It is "add one durable HTTP ingress path that feeds the existing processor." Everything else should be a thin adapter.
- Complexity smell check: the plan touches too many conceptual layers at once, and at least one of them is invented. `internal/pipeline/filter.go` already exists in the repo, so the plan's "split the current Discord-specific Candidate shape from the shared capture concept" is a refactor of an existing seam, not a new file-level seam.
- Overbuilt parts: the Android package split (`app/capture/upload/ui`) is probably too much structure for an MVP whose only job is instant record + upload + sent/error state. Also, a lock-screen path should stay optional; the plan already says that, which is good.

## 2. Findings First

1. High: the ingest durability model is missing. The plan says the HTTP handler will "save the raw file to disk before returning success" and "enqueue processing and return fast" (`docs/android-voice-inbox-plan.md:165-175`), but it never defines what makes that queue durable. If the queue is in-memory, a daemon restart drops uploads. If the handler returns before a durable record exists, a crash leaves orphaned audio with no retry path. This needs an explicit persisted capture record and worker contract before the first 200 OK.
2. High: the plan is pointing at the wrong abstraction boundary. `internal/pipeline/filter.go` already contains the shared `Candidate` and filtering logic, while `internal/pipeline/runner.go:452-565` already contains the shared process flow. The plan's proposed new `filter.go` seam risks creating a second parallel pipeline instead of extending the existing one. The better move is to introduce a source-neutral capture record/helper next to the current pipeline, not to invent a new shared layer.
3. Medium: the plan does not account for concurrent execution with the existing daemon controls. `runner.go:87-110`, `224-247`, and `344-350` show that poll/retry/cleanup currently serialize through `state.AcquireFileLock`. If HTTP ingest processing happens in another goroutine or worker without the same coordination, it can race with poll/retry, duplicate journal appends, or interleave state writes. The plan needs to say whether ingest shares the same single-flight lock, a separate queue, or both.

## 3. Architecture Review

- Data flow should be: phone -> HTTP ingest -> durable capture row + raw file -> source-neutral processor -> transcribe/journal -> retry/cleanup. The capture row should be the source of truth for idempotency and recovery.
- Boundary choices: keep one Mac-side binary. Add one ingest surface and one shared processor path. Do not add a second "framework" around the pipeline just to host Android input.
- Reversibility: make ingest opt-in behind a new subcommand or flag, and keep Discord poll/retry untouched until ingest passes parity tests. That keeps rollback simple.
- Boring-by-default assessment: the plan is mostly boring in the right ways, but the Android module split and the new backend file layout are a bit too eager. The simplest solution that still survives restarts is better than a prettier architecture that only works while the process stays up.

## 4. Test Review

- The proposed test matrix is directionally right, but it is incomplete for the failure modes that matter here. `internal/http/ingest_test.go` should cover auth, upload parsing, idempotency, and happy path, but the plan also needs restart recovery, oversized body rejection, malformed multipart, duplicate uploads racing at the same time, and the poll/retry collision case.
- The current repo already has useful seams to extend: `internal/pipeline/filter_test.go`, `internal/pipeline/runner_integration_test.go`, `internal/state/store.go` tests if added, `internal/journal/journal_test.go`, and `internal/obsidian/client_status_test.go`.
- ASCII test map:

```text
                +---------------------+
                | ingest handler test |
                | auth/body/multipart |
                +----------+----------+
                           |
                 durable row + raw file
                           |
                +----------v----------+
                | processor/recovery   |
                | restart/idempotency  |
                +----+------------+---+
                     |            |
             journal append   retry/cleanup
                     |            |
          runner integration   store tests
```

- Missing from the plan: a store migration test that proves old Discord rows still load, and a concurrency test that shows HTTP ingest cannot corrupt the existing retry queue.

## 5. Opinionated Recommendations

- Narrow the backend work to one new persisted capture model plus one shared processor helper. Do not create a new broad "shared capture pipeline" abstraction unless the code actually forces it.
- Replace the planned `internal/http` package name with something less generic, like `internal/ingest` or `internal/server`, so it is clear this is a thin adapter, not a whole web stack.
- If you want the minimal correct backend, add a separate `captures` table or equivalent source-neutral record instead of forcing HTTP uploads into the Discord `messages` primary key shape. That keeps idempotency and source metadata explicit.
- Keep the Android app as one module and one main flow. Record, stop, upload, sent/error. Skip any extra navigation or package layering until the capture habit exists.

## 6. Final Verdict

DONE_WITH_CONCERNS

The plan is viable, but only if it is narrowed around a durable ingest record and an explicit concurrency model. As written, it risks a fast-looking HTTP endpoint that loses uploads on restart or races the existing daemon work.
