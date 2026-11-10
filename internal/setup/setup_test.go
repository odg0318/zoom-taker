package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odg0318/zoom-taker/internal/config"
)

func testConfig(base string) config.Config {
	return config.Config{
		ModelCacheDir: filepath.Join(base, "model-cache"),
		RecordingsDir: filepath.Join(base, "recordings"),
		OutputDir:     filepath.Join(base, "output"),
		Model:         "ggml-test.bin",
	}
}

func TestEnsureCreatesDirsAndSkipsCachedModel(t *testing.T) {
	base := t.TempDir()
	cfg := testConfig(base)

	// Pre-cache the model the way a completed download leaves it: bin + meta.
	if err := os.MkdirAll(cfg.ModelCacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(cfg.ModelCacheDir, "ggml-test.bin")
	if err := os.WriteFile(bin, []byte("12345678"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]int64{"size": 8})
	if err := os.WriteFile(filepath.Join(cfg.ModelCacheDir, "ggml-test.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}

	var lines []string
	log := func(s string) { lines = append(lines, s) }
	if err := Ensure(context.Background(), cfg, nil, false, log); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{cfg.RecordingsDir, cfg.OutputDir} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("dir %s not created: %v", d, err)
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "model cached") {
		t.Fatalf("log missing cached-model line: %v", lines)
	}
	if strings.Contains(joined, "download") {
		t.Fatalf("cached model must not trigger a download prompt: %v", lines)
	}
}

func TestEnsureMissingPathModelWarns(t *testing.T) {
	// A path model is never fetched by the wizard — a missing one must warn
	// and still leave the data dirs in place, without any network activity.
	base := t.TempDir()
	cfg := testConfig(base)
	cfg.Model = filepath.Join(base, "missing.bin")

	var lines []string
	log := func(s string) { lines = append(lines, s) }
	if err := Ensure(context.Background(), cfg, nil, false, log); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "model file not found") {
		t.Fatalf("log missing missing-model warning: %v", lines)
	}
	for _, d := range []string{cfg.ModelCacheDir, cfg.RecordingsDir, cfg.OutputDir} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("dir %s not created: %v", d, err)
		}
	}
}

func TestEnsureDeclinedDownload(t *testing.T) {
	base := t.TempDir()
	cfg := testConfig(base)
	cfg.Model = "ggml-large-v3-turbo-q5_0.bin" // never cached here

	var lines []string
	log := func(s string) { lines = append(lines, s) }
	// Answer "n": Ensure must return nil without touching the network.
	if err := Ensure(context.Background(), cfg, strings.NewReader("n\n"), true, log); err != nil {
		t.Fatalf("declined download must not error: %v", err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "skipped") {
		t.Fatalf("log missing skipped line: %v", lines)
	}
}

func TestConfirm(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"\n", true},
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"n\n", false},
		{"N\n", false},
		{"no\n", false},
		{"whatever\n", false},
	}
	for _, c := range cases {
		if got := confirm(strings.NewReader(c.in)); got != c.want {
			t.Errorf("confirm(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	// EOF takes the default (yes).
	if !confirm(strings.NewReader("")) {
		t.Error("confirm(EOF) = false, want default true")
	}
	if !confirm(nil) {
		t.Error("confirm(nil) = false, want default true")
	}
}
