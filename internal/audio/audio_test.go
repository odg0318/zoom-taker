package audio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fixtures are generated with ffmpeg (the same dependency the wrappers use);
// tests skip silently when ffmpeg is absent.
var tonePath, silencePath string

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "audio-test")
	if err != nil {
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	tonePath = filepath.Join(dir, "tone.wav")       // 440Hz @ ~-31dBFS, 6s
	silencePath = filepath.Join(dir, "silence.wav") // digital silence, 4s
	gen := func(args ...string) error {
		return exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).Run()
	}
	if err := gen("-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-af", "volume=0.3", "-ar", "16000", "-ac", "1", tonePath); err != nil {
		fmt.Fprintln(os.Stderr, "fixture gen:", err)
		os.Exit(1)
	}
	if err := gen("-f", "lavfi", "-i", "anullsrc=r=16000:cl=mono",
		"-t", "4", silencePath); err != nil {
		fmt.Fprintln(os.Stderr, "fixture gen:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestDuration(t *testing.T) {
	d, err := Duration(tonePath)
	if err != nil {
		t.Fatal(err)
	}
	if d < 5.5 || d > 6.5 {
		t.Fatalf("duration = %v, want ~6", d)
	}
}

func TestRMSDB(t *testing.T) {
	ctx := context.Background()
	db, err := RMSDB(ctx, tonePath, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if db < -40 || db > -25 {
		t.Fatalf("tone rms = %gdB, want ~-31dBFS (ffmpeg sine @ volume 0.3)", db)
	}
	quiet, err := RMSDB(ctx, silencePath, 0.5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if quiet != -120.0 {
		t.Fatalf("silence rms = %v, want -120", quiet)
	}
}

func TestSilenceRanges(t *testing.T) {
	ctx := context.Background()
	got, err := SilenceRanges(ctx, silencePath, -45, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("silence.wav ranges = %v, want 1", got)
	}
	if got[0].Start > 1 || got[0].End < 3 {
		t.Fatalf("silence.wav range = %v, want ~[0, 4]", got[0])
	}
	none, err := SilenceRanges(ctx, tonePath, -45, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("tone.wav ranges = %v, want none", none)
	}
}

func TestDecode(t *testing.T) {
	samples, err := Decode(context.Background(), tonePath)
	if err != nil {
		t.Fatal(err)
	}
	// 6s @ 16kHz mono, ±1% tolerance
	if n := len(samples); n < 94000 || n > 97000 {
		t.Fatalf("decoded %d samples, want ~96000", n)
	}
}
