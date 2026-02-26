package pipeline

import "time"

type Result struct {
	Command    string         `json:"command"`
	RunID      string         `json:"run_id,omitempty"`
	Processed  int            `json:"processed"`
	Succeeded  int            `json:"succeeded"`
	Failed     int            `json:"failed"`
	Requeued   int            `json:"requeued"`
	Skipped    int            `json:"skipped"`
	DurationMS int64          `json:"duration_ms"`
	Errors     []string       `json:"errors,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

func (r Result) ExitCode() int {
	if r.Failed == 0 {
		return 0
	}
	if r.Succeeded > 0 {
		return 2
	}
	return 1
}

func finalizeResult(r *Result, startedAt time.Time) {
	r.DurationMS = time.Since(startedAt).Milliseconds()
}
