# Android Voice Inbox App — Implementation Spec

Date: 2026-03-20
Status: ready for implementation
Related: [android-voice-inbox-plan.md](android-voice-inbox-plan.md)
Reference: [Codex voice.rs](https://github.com/openai/codex/blob/main/codex-rs/tui_app_server/src/voice.rs)

## Architecture Overview

```text
[Pixel 8a]
  Quick Settings tile / launcher
          |
          v
[Android App]
  AudioRecord → PCM16 24kHz mono → WAV
  Stop → OpenAI API transcription (gpt-4o-mini-transcribe)
  Upload raw audio + transcript → voice-inbox-daemon
          |
          v
[voice-inbox-daemon serve]
  Receive capture with pre-transcribed text
  Skip Whisper if transcript provided
  Append to Obsidian Journal
```

Key difference from original plan: Android handles transcription via OpenAI API,
daemon receives both raw audio (backup) and transcript (ready to use).

## Tech Stack

| Component | Choice | Rationale |
|-----------|--------|-----------|
| Language | Kotlin | Standard Android |
| UI | Jetpack Compose | Modern, minimal UI needed |
| Audio capture | AudioRecord | Low-level control, PCM16 output |
| Transcription | OpenAI API `gpt-4o-mini-transcribe` | Same as Codex, fast and accurate |
| HTTP | OkHttp | Multipart upload, reliable |
| Background upload | WorkManager | Survives process death |
| Local queue | Room DB | Pending uploads with retry state |
| Settings | DataStore | Server URL, auth token, OpenAI key |
| Quick Settings | TileService | Primary launch path |
| Min SDK | 34 (Android 14) | Pixel 8a target |
| Target SDK | 35 | Latest |

## Repository Structure

```
~/Develop/android-voice-inbox/
├── app/
│   ├── src/main/
│   │   ├── java/com/kai/voiceinbox/
│   │   │   ├── MainActivity.kt           # Single activity, Compose host
│   │   │   ├── VoiceInboxApp.kt          # Application class
│   │   │   │
│   │   │   ├── capture/
│   │   │   │   ├── AudioRecorder.kt      # AudioRecord wrapper, PCM16 24kHz mono
│   │   │   │   ├── WavEncoder.kt         # PCM → WAV with peak normalization
│   │   │   │   └── CaptureManager.kt     # Orchestrates record → transcribe → upload
│   │   │   │
│   │   │   ├── transcribe/
│   │   │   │   └── OpenAITranscriber.kt  # POST /v1/audio/transcriptions
│   │   │   │
│   │   │   ├── upload/
│   │   │   │   ├── IngestClient.kt       # POST /v0/captures to daemon
│   │   │   │   └── UploadWorker.kt       # WorkManager for retry
│   │   │   │
│   │   │   ├── data/
│   │   │   │   ├── CaptureEntity.kt      # Room entity
│   │   │   │   ├── CaptureDao.kt         # Room DAO
│   │   │   │   └── AppDatabase.kt        # Room DB
│   │   │   │
│   │   │   ├── tile/
│   │   │   │   └── VoiceInboxTile.kt     # Quick Settings TileService
│   │   │   │
│   │   │   ├── ui/
│   │   │   │   ├── RecordScreen.kt       # Recording UI (timer, stop button, amplitude)
│   │   │   │   ├── SendingScreen.kt      # Upload progress
│   │   │   │   ├── SentScreen.kt         # Brief confirmation
│   │   │   │   ├── ErrorScreen.kt        # Error + retry
│   │   │   │   └── SettingsScreen.kt     # Server URL, tokens (minimal)
│   │   │   │
│   │   │   └── settings/
│   │   │       └── AppSettings.kt        # DataStore wrapper
│   │   │
│   │   ├── res/
│   │   │   ├── drawable/                 # Tile icon, app icon
│   │   │   └── values/
│   │   │       └── strings.xml
│   │   │
│   │   └── AndroidManifest.xml
│   │
│   └── build.gradle.kts
│
├── gradle/
├── build.gradle.kts                       # Root build
├── settings.gradle.kts
└── gradle.properties
```

## Audio Capture (following Codex voice.rs)

### AudioRecorder.kt

```
- AudioRecord with:
  - source: MediaRecorder.AudioSource.MIC
  - sampleRate: 24000  (matches OpenAI model)
  - channelConfig: AudioFormat.CHANNEL_IN_MONO
  - encoding: AudioFormat.ENCODING_PCM_16BIT
  - bufferSize: AudioRecord.getMinBufferSize(...) * 2

- State: IDLE → RECORDING → STOPPED
- During recording:
  - Read PCM16 samples into growing buffer (ShortArray)
  - Track peak amplitude for UI meter (atomic, per-read update)
  - Enforce minimum 1 second duration before allowing stop

- stop() returns RecordedAudio(samples: ShortArray, sampleRate: Int)
```

### WavEncoder.kt (from Codex encode_wav_normalized)

```
- Input: ShortArray samples, sampleRate=24000, channels=1
- Peak normalization: gain = (0.9 * Short.MAX_VALUE) / peakAbs
- Output: ByteArray of valid WAV file
- WAV header: RIFF/WAVE, fmt chunk (PCM16, 24kHz, mono), data chunk
```

## Transcription (OpenAI API)

### OpenAITranscriber.kt

```
- POST https://api.openai.com/v1/audio/transcriptions
- Headers:
  - Authorization: Bearer {OPENAI_API_KEY}
  - Content-Type: multipart/form-data
- Body:
  - file: audio.wav (the WAV bytes)
  - model: "gpt-4o-mini-transcribe"
  - language: "ja"
- Response: { "text": "transcribed text..." }
- Timeout: 30 seconds
- No retry here — if it fails, upload raw audio without transcript
```

## Upload to Daemon

### IngestClient.kt

```
- POST {serverUrl}/v0/captures
- Headers:
  - Authorization: Bearer {INGEST_AUTH_TOKEN}
  - Content-Type: multipart/form-data
- Body:
  - audio: WAV file bytes
  - capture_id: UUID generated at recording time (stable for retries)
  - device_id: "pixel8a" (from Build.MODEL)
  - captured_at: ISO8601 timestamp of recording start
  - transcript: transcribed text (optional, new field)
- Response: 201 Created or 200 OK (duplicate)
```

**Backend change needed**: Add optional `transcript` field to `POST /v0/captures`.
If transcript is provided, daemon can skip Whisper and use it directly.

## Local Capture Queue (Room DB)

### CaptureEntity

```kotlin
@Entity(tableName = "captures")
data class CaptureEntity(
    @PrimaryKey val captureId: String,      // UUID
    val audioPath: String,                   // Local WAV file path
    val transcript: String?,                 // From OpenAI API (null if failed)
    val capturedAt: String,                  // ISO8601
    val status: String,                      // pending | uploading | done | failed
    val attempts: Int = 0,
    val lastError: String? = null,
    val createdAt: Long = System.currentTimeMillis()
)
```

### Upload Flow

```
Recording complete
    → Save WAV to app internal storage
    → Insert CaptureEntity(status=pending)
    → Start transcription (OpenAI API)
    → Update entity with transcript (or leave null on failure)
    → Enqueue UploadWorker
    → UploadWorker: POST /v0/captures
    → On success: status=done, delete local WAV
    → On failure: status=failed, schedule retry via WorkManager backoff
```

### WorkManager Retry

```
- ExponentialBackoffPolicy
- Initial delay: 30 seconds
- Max retries: 8
- Constraints: NetworkType.CONNECTED
```

## UI States (Jetpack Compose)

### State Machine

```
IDLE → (tile tap or launch) → RECORDING
RECORDING → (stop button) → TRANSCRIBING
TRANSCRIBING → (API complete) → UPLOADING
UPLOADING → (success) → SENT → (2s auto-dismiss) → IDLE
UPLOADING → (failure) → ERROR
ERROR → (retry tap) → UPLOADING
ERROR → (dismiss) → IDLE
```

### RecordScreen

```
- Full screen, dark background
- Center: elapsed time counter (MM:SS)
- Center: amplitude visualizer (simple circle/bar that pulses with peak)
- Bottom: large STOP button (red circle)
- No title input, no settings, no other controls
```

### SendingScreen

```
- "Transcribing..." with spinner (during OpenAI call)
- "Sending..." with spinner (during upload)
- No cancel button
```

### SentScreen

```
- Checkmark icon
- "Sent" text
- Auto-dismiss after 2 seconds → finish activity
```

### ErrorScreen

```
- Error icon
- Brief error message
- "Retry" button
- "Close" button (queues for background retry)
```

## Quick Settings Tile

### VoiceInboxTile.kt

```kotlin
class VoiceInboxTile : TileService() {
    override fun onClick() {
        // Launch MainActivity with RECORD action
        val intent = Intent(this, MainActivity::class.java).apply {
            action = "com.kai.voiceinbox.RECORD"
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        startActivityAndCollapse(intent)
    }

    override fun onStartListening() {
        qsTile.state = Tile.STATE_INACTIVE
        qsTile.label = "Voice Inbox"
        qsTile.updateTile()
    }
}
```

## Settings (Minimal)

### First Launch / Settings Screen

Only shown if server URL or tokens are not configured:

- Server URL (e.g., `http://100.x.x.x:8787` for Tailscale)
- Ingest Auth Token
- OpenAI API Key
- Save button

Store in encrypted DataStore.

## Manifest Permissions

```xml
<uses-permission android:name="android.permission.RECORD_AUDIO" />
<uses-permission android:name="android.permission.INTERNET" />
<uses-permission android:name="android.permission.FOREGROUND_SERVICE" />
<uses-permission android:name="android.permission.FOREGROUND_SERVICE_MICROPHONE" />
```

## Acceptance Criteria

1. Tile tap → recording starts within 500ms
2. Stop → transcription + upload completes (happy path < 10s)
3. No typing required in entire flow
4. Failed upload is recoverable (WorkManager retry)
5. Duplicate upload (same capture_id) doesn't create duplicate journal entry
6. App works over Tailscale network
7. Audio format: WAV, PCM16, 24kHz, mono (OpenAI optimized)
8. Minimum 1 second recording enforced

## Backend Extension Needed

Add optional `transcript` field to `POST /v0/captures`:
- If `transcript` is present, skip Whisper transcription in the pipeline
- Store transcript directly, proceed to Journal append
- Still store raw audio for backup

This is a small addition to `internal/ingest/server.go` and `internal/pipeline/runner.go`.

## Not in v0.0.1

- Transcript preview/editing
- Recording list / history
- Multi-destination support
- Waveform visualization (simple amplitude only)
- Lock screen launch (investigate post-v0.0.1)
- Offline transcription
- Widget (Quick Settings tile is sufficient)
