package transcribe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type WhisperConfig struct {
	Bin      string
	Model    string
	Language string
}

type Result struct {
	Text           string
	TranscriptJSON string
	WavPath        string
}

func NormalizeToWav(ctx context.Context, ffmpegBin, inputPath, outputWavPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputWavPath), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(
		ctx,
		ffmpegBin,
		"-y",
		"-i", inputPath,
		"-ac", "1",
		"-ar", "16000",
		"-c:a", "pcm_s16le",
		outputWavPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func RunWhisper(ctx context.Context, cfg WhisperConfig, wavPath, outputDir string) (Result, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(
		ctx,
		cfg.Bin,
		wavPath,
		"--model", cfg.Model,
		"--language", cfg.Language,
		"--output_format", "json",
		"--output_dir", outputDir,
		"--verbose", "False",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Result{}, fmt.Errorf("whisper failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	base := strings.TrimSuffix(filepath.Base(wavPath), filepath.Ext(wavPath))
	jsonPath := filepath.Join(outputDir, base+".json")
	text, err := extractWhisperText(jsonPath)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Text:           strings.TrimSpace(text),
		TranscriptJSON: jsonPath,
		WavPath:        wavPath,
	}, nil
}

func extractWhisperText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read whisper json: %w", err)
	}

	var payload struct {
		Text     string `json:"text"`
		Segments []struct {
			Text string `json:"text"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("parse whisper json: %w", err)
	}

	if strings.TrimSpace(payload.Text) != "" {
		return payload.Text, nil
	}

	var b strings.Builder
	for _, seg := range payload.Segments {
		s := strings.TrimSpace(seg.Text)
		if s == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(s)
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("whisper json has no text segments")
	}
	return b.String(), nil
}

func ContextWithTranscriptionTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 10*time.Minute)
}
