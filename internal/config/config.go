package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	DiscordBotToken         string
	DiscordAPIBaseURL       string
	VoiceInboxChannelID     string
	AllowedAuthorIDs        map[string]struct{}
	AllowedAuthorIDsList    []string
	DiscordFetchLimit       int
	PollIntervalSeconds     int
	WhisperBin              string
	WhisperModel            string
	WhisperLanguage         string
	FFmpegBin               string
	ObsidianBaseURL         string
	ObsidianAPIKey          string
	ObsidianAuthHeader      string
	ObsidianVerifyTLS       bool
	VaultJournalDir         string
	AudioRetentionDays      int
	TranscriptRetentionDays int
	MaxRetryAttempts        int
	RetryBaseSeconds        int
	RetryMaxSeconds         int
	StateDBPath             string
	AudioStoreDir           string
	LogDir                  string
	LockFilePath            string
}

func Load() (Config, error) {
	_ = loadDotEnv(".env")

	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve home: %w", err)
	}

	stateDefault := filepath.Join(home, "Library", "Application Support", "voice-inbox-daemon", "state.db")
	audioDefault := filepath.Join(home, "Library", "Application Support", "voice-inbox-daemon", "audio")
	logDefault := filepath.Join(home, "Library", "Logs", "voice-inbox-daemon")

	cfg := Config{
		DiscordBotToken:         strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN")),
		DiscordAPIBaseURL:       strings.TrimRight(getEnvDefault("DISCORD_API_BASE_URL", "https://discord.com/api/v10"), "/"),
		VoiceInboxChannelID:     getEnvDefault("VOICE_INBOX_CHANNEL_ID", "1476388224124325909"),
		DiscordFetchLimit:       getEnvInt("DISCORD_FETCH_LIMIT", 100),
		PollIntervalSeconds:     getEnvInt("POLL_INTERVAL_SECONDS", 300),
		WhisperBin:              getEnvDefault("WHISPER_BIN", "/opt/homebrew/bin/whisper"),
		WhisperModel:            getEnvDefault("WHISPER_MODEL", "large-v3-turbo"),
		WhisperLanguage:         getEnvDefault("WHISPER_LANGUAGE", "ja"),
		FFmpegBin:               getEnvDefault("FFMPEG_BIN", "/opt/homebrew/bin/ffmpeg"),
		ObsidianBaseURL:         strings.TrimRight(getEnvDefault("OBSIDIAN_BASE_URL", "https://127.0.0.1:27124"), "/"),
		ObsidianAPIKey:          strings.TrimSpace(os.Getenv("OBSIDIAN_API_KEY")),
		ObsidianAuthHeader:      getEnvDefault("OBSIDIAN_AUTH_HEADER", "Authorization"),
		ObsidianVerifyTLS:       getEnvBool("OBSIDIAN_VERIFY_TLS", false),
		VaultJournalDir:         strings.Trim(getEnvDefault("VAULT_JOURNAL_DIR", "01_Projects/Journal"), "/"),
		AudioRetentionDays:      getEnvInt("AUDIO_RETENTION_DAYS", 14),
		TranscriptRetentionDays: getEnvInt("TRANSCRIPT_RETENTION_DAYS", 7),
		MaxRetryAttempts:        getEnvInt("MAX_RETRY_ATTEMPTS", 8),
		RetryBaseSeconds:        getEnvInt("RETRY_BASE_SECONDS", 300),
		RetryMaxSeconds:         getEnvInt("RETRY_MAX_SECONDS", 86400),
		StateDBPath:             expandPath(getEnvDefault("STATE_DB_PATH", stateDefault), home),
		AudioStoreDir:           expandPath(getEnvDefault("AUDIO_STORE_DIR", audioDefault), home),
		LogDir:                  expandPath(getEnvDefault("LOG_DIR", logDefault), home),
	}

	allowedRaw := getEnvDefault("VOICE_INBOX_ALLOWED_AUTHOR_IDS", "968754117885456425")
	cfg.AllowedAuthorIDs, cfg.AllowedAuthorIDsList = parseCSVSet(allowedRaw)
	cfg.LockFilePath = cfg.StateDBPath + ".lock"

	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func validate(cfg Config) error {
	var problems []string

	if cfg.DiscordBotToken == "" {
		problems = append(problems, "DISCORD_BOT_TOKEN is required")
	}
	if cfg.VoiceInboxChannelID == "" {
		problems = append(problems, "VOICE_INBOX_CHANNEL_ID is required")
	}
	if cfg.DiscordAPIBaseURL == "" {
		problems = append(problems, "DISCORD_API_BASE_URL must not be empty")
	}
	if len(cfg.AllowedAuthorIDs) == 0 {
		problems = append(problems, "VOICE_INBOX_ALLOWED_AUTHOR_IDS must include at least one author ID")
	}
	if cfg.ObsidianBaseURL == "" {
		problems = append(problems, "OBSIDIAN_BASE_URL is required")
	}
	if cfg.ObsidianAPIKey == "" {
		problems = append(problems, "OBSIDIAN_API_KEY is required")
	}
	if cfg.DiscordFetchLimit <= 0 {
		problems = append(problems, "DISCORD_FETCH_LIMIT must be > 0")
	}
	if cfg.PollIntervalSeconds <= 0 {
		problems = append(problems, "POLL_INTERVAL_SECONDS must be > 0")
	}
	if cfg.AudioRetentionDays <= 0 {
		problems = append(problems, "AUDIO_RETENTION_DAYS must be > 0")
	}
	if cfg.MaxRetryAttempts <= 0 {
		problems = append(problems, "MAX_RETRY_ATTEMPTS must be > 0")
	}
	if cfg.RetryBaseSeconds <= 0 || cfg.RetryMaxSeconds <= 0 {
		problems = append(problems, "RETRY_BASE_SECONDS and RETRY_MAX_SECONDS must be > 0")
	}
	if cfg.RetryBaseSeconds > cfg.RetryMaxSeconds {
		problems = append(problems, "RETRY_BASE_SECONDS must be <= RETRY_MAX_SECONDS")
	}
	if cfg.VaultJournalDir == "" {
		problems = append(problems, "VAULT_JOURNAL_DIR must not be empty")
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func getEnvDefault(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func getEnvInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

func getEnvBool(key string, def bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

func parseCSVSet(raw string) (map[string]struct{}, []string) {
	set := make(map[string]struct{})
	list := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, exists := set[id]; exists {
			continue
		}
		set[id] = struct{}{}
		list = append(list, id)
	}
	return set, list
}

func expandPath(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}
	return p
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, "\"")
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}
	return scanner.Err()
}
