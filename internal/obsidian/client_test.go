package obsidian

import "testing"

func TestEncodeVaultPath(t *testing.T) {
	got := encodeVaultPath("01_Projects/Journal/2026-02-26 日記.md")
	want := "01_Projects/Journal/2026-02-26%20%E6%97%A5%E8%A8%98.md"
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}
