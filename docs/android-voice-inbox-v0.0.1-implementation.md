# Android Voice Inbox v0.0.1 Implementation

このドキュメントは、`voice-inbox-daemon` に入れた Android Voice Inbox backend 実装を、**パス単位**で追えるようにまとめたものです。

対象スコープ:

- `serve` コマンド追加
- HTTP ingest 追加
- `captures` 永続化追加
- 既存 `Runner` への shared processing 統合
- Journal メタデータ拡張
- テスト追加
- README / operations 追記

## 全体像

v0.0.1 で追加した backend フローは次の通りです。

1. Android クライアントが `POST /v0/captures` に multipart upload
2. サーバーが raw audio を保存
3. サーバーが SQLite `captures` に durable registration
4. `voice-inbox serve` の in-process worker が capture を処理
5. 既存の文字起こし / Journal append 経路に流す
6. `captures.status=done` に更新

重要な設計判断:

- HTTP ingest は **Journal に直接書かない**
- HTTP capture も Discord message も **`Runner` の shared processor** で処理する
- processing 系は既存どおり **file lock** で serialize する

## Path-Level Breakdown

### [cmd/voice-inbox/main.go](/Users/kai/Develop/voice-inbox-daemon/cmd/voice-inbox/main.go)

責務:

- 新コマンド `serve` の追加
- `config.LoadForCommand(cmd)` への切り替え
- ingest server 起動
- capture worker loop 起動

実装したこと:

- `serve` を usage と command switch に追加
- `runServe(...)` を新設
- `internal/ingest` を import
- `http.Server` を立てて `cfg.IngestListenAddr` で listen
- `signal.NotifyContext` で graceful shutdown
- goroutine で `runner.ProcessCapturesOnce(...)` を定期実行

ここで決めたこと:

- `serve` 自体が backend の entrypoint
- capture 処理は別プロセスではなく、まずは `serve` の中で回す
- worker は「シンプルで boring」な fixed ticker

### [internal/config/config.go](/Users/kai/Develop/voice-inbox-daemon/internal/config/config.go)

責務:

- ingest 用設定の追加
- command ごとの validation 分岐

追加した config:

- `IngestListenAddr`
- `IngestAuthToken`
- `IngestMaxBodyMB`
- `IngestSourceName`

実装したこと:

- `LoadForCommand(command string)` を追加
- `serve` の時だけ Discord 必須 env を免除
- `serve` の時は `INGEST_AUTH_TOKEN` を必須化
- `INGEST_LISTEN_ADDR` 空文字禁止
- ingest 向け env の default を追加

意図:

- `serve` は Discord bot token なしでも起動できるようにする
- 既存 `poll/retry/cleanup` の validation は維持する

### [internal/ingest/server.go](/Users/kai/Develop/voice-inbox-daemon/internal/ingest/server.go)

責務:

- HTTP ingest の request boundary
- auth / multipart parse / file persist / durable capture registration

公開 surface:

- `GET /healthz`
- `POST /v0/captures`

`POST /v0/captures` contract:

- `Authorization: Bearer <INGEST_AUTH_TOKEN>`
- `multipart/form-data`
- fields:
  - `audio`
  - `capture_id`
  - `device_id`
  - `captured_at`
  - `source_dedupe_key` (optional)

実装したこと:

- bearer token 認証
- `http.MaxBytesReader` による body size 制限
- multipart form parse
- `capture_id` 必須
- `captured_at` は RFC3339 parse
- raw file を `AUDIO_STORE_DIR/ingest/YYYY/MM/DD/...` に保存
- その後 `captures` table に insert
- duplicate request 時は既存 row を返す

重要な保証:

- **raw file 保存 + SQLite 登録の両方が終わるまで success を返さない**

現在の response:

- 新規作成: `201 Created`
- duplicate: `200 OK`

response body:

- `capture_id`
- `status`
- `source`
- `duplicate`
- `received_at`

### [internal/ingest/server_test.go](/Users/kai/Develop/voice-inbox-daemon/internal/ingest/server_test.go)

責務:

- ingest endpoint の boundary test

カバーしていること:

- bearer token 必須
- malformed multipart を reject
- raw file と DB row が保存される
- `capture_id` duplicate が idempotent
- response JSON shape が期待どおり

意図:

- Android client 実装前に server contract を固定する

### [internal/state/store.go](/Users/kai/Develop/voice-inbox-daemon/internal/state/store.go)

責務:

- HTTP origin capture の durable model 追加
- capture retry / cleanup / status 集計の永続化

追加した model:

- `CaptureRecord`

schema 追加:

- `captures` table
  - `capture_id`
  - `source`
  - `source_dedupe_key`
  - `device_id`
  - `captured_at`
  - `received_at`
  - `raw_audio_path`
  - `content_type`
  - `status`
  - `attempts`
  - `next_retry_at`
  - `journal_path`
  - `transcript_path`
  - `last_error`
  - timestamps

index 追加:

- `idx_captures_source_dedupe_key`

追加した store API:

- `CreateCapture`
- `GetCapture`
- `GetCaptureBySourceDedupeKey`
- `RecoverStuckCaptures`
- `ListCapturesForProcessing`
- `MarkCaptureProcessing`
- `MarkCaptureDone`
- `MarkCaptureFailed`
- `ListDoneCapturesWithAudioBefore`
- `ListDoneCapturesWithTranscriptBefore`
- `ClearCaptureAudioPath`
- `ClearCaptureTranscriptPath`

既存 summary 拡張:

- `CaptureTotal`
- `CaptureByStatus`
- `CaptureRetryDue`

schema version:

- `schema_version = 2`

意図:

- Discord `messages` table に HTTP upload を押し込まない
- HTTP ingest の idempotency と retry を source-neutral に持つ

### [internal/pipeline/runner.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/runner.go)

責務:

- Discord と HTTP capture の **shared processing owner**

追加した内部 abstraction:

- `processTarget`
- `processArtifacts`

追加した entrypoint:

- `ProcessCapturesOnce(ctx)`

追加した内部処理:

- `processReadyCaptures(...)`
- `processStoredCapture(...)`
- `processTarget(...)`
- `scheduleCaptureFailure(...)`

変更した点:

- `PollOnce` の最後で `processRetryCandidates` と `processReadyCaptures` を回す
- `Retry` でも `processReadyCaptures` を回す
- `Cleanup` に capture artifact cleanup を追加
- Discord path の `processCandidate(...)` も `processTarget(...)` を使うように寄せた

`processTarget(...)` が担うこと:

- text / audio の分岐
- audio normalization
- whisper transcription
- journal file existence check
- Journal entry build
- duplicate append 防止
- artifact path の返却

source ごとの差:

- Discord:
  - 最後に reaction を付ける
  - `messages` table を更新
- HTTP capture:
  - reaction なし
  - `captures` table を更新

意図:

- Journal append の責務を 1 箇所に寄せる
- 2 本目の parallel pipeline を作らない

### [internal/pipeline/runner_integration_test.go](/Users/kai/Develop/voice-inbox-daemon/internal/pipeline/runner_integration_test.go)

責務:

- shared processing が Discord / capture の両方で動くことを検証

既存 coverage:

- Discord audio path
- Discord text path
- reaction retry
- retry due processing

追加した coverage:

- `ProcessCapturesOnce` で pending capture を処理できる
- capture の Journal append と transcript 保存
- capture failure -> retry -> recovery
- duplicate append を防げる
- stuck `processing` capture を recover できる

意図:

- shared processor 化による回帰を押さえる

### [internal/journal/journal.go](/Users/kai/Develop/voice-inbox-daemon/internal/journal/journal.go)

責務:

- Journal metadata を source-neutral に広げる

`EntryInput` 追加 fields:

- `Source`
- `CaptureID`
- `DeviceID`
- `CapturedAt`

entry YAML に追加したもの:

- `source`
- `capture_id`
- `device_id`
- `captured_at`

維持したもの:

- `discord_channel_id`
- `discord_message_id`
- `discord_author_id`
- `discord_jump_url`
- `audio_file`
- `whisper_model`
- `processed_at`

意図:

- Discord 起源のエントリも HTTP 起源のエントリも、同じ `voice_inbox` block で見られるようにする

### [internal/journal/journal_test.go](/Users/kai/Develop/voice-inbox-daemon/internal/journal/journal_test.go)

責務:

- Journal metadata 拡張の snapshot 的検証

追加した assert:

- `source: "discord"`
- `capture_id: "123"`

### [README.md](/Users/kai/Develop/voice-inbox-daemon/README.md)

追記したこと:

- `serve` command の紹介
- HTTP ingest の最小 contract
- ingest 用 env 一覧
- durable registration の説明

意図:

- リポジトリ README だけ読んでも v0.0.1 backend を起動できるようにする

### [docs/OPERATIONS.md](/Users/kai/Develop/voice-inbox-daemon/docs/OPERATIONS.md)

追記したこと:

- `serve` の起動例
- ingest 用 env の説明
- durable registration の運用メモ
- `poll` が retry due を吸い上げる補足

意図:

- 運用手順書として `serve` を扱える状態にする

## 実装していないもの

v0.0.1 ではまだ入れていないもの:

- Android client 本体
- Quick Settings tile / lock-screen 導線
- real-device curl / Android からの live smoke test
- concurrent duplicate upload の race test
- `.wav` artifact の専用 cleanup policy
- `serve` worker interval の tunable 化
- ingest success 後の push-style wakeup 最適化

## 既知の残リスク

1. `serve` の drain loop は fixed ticker で、まだ supervisor 設計を切り出していない
2. duplicate handling は通常系では test 済みだが、同一 `capture_id` 同時送信 race は未検証
3. DB insert 失敗時に raw file が orphan するケースは v0.0.1 では未回収
4. `.wav` intermediate は既存挙動のままで、今回新規に cleanup していない

## 検証状況

実施済み:

- `go test ./...`

未実施:

- live Obsidian / live ffmpeg / live whisper を使った end-to-end smoke
- Android 実機 upload

## 次に自然な作業

1. `curl` で `POST /v0/captures` の手動 smoke
2. launchd か別 supervisor で `voice-inbox serve` の常駐化
3. Android app の最小 capture client 実装
4. Pixel 8a の起動導線最適化
