package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryDefaults(t *testing.T) {
	for _, k := range []string{
		"ZOOM_TAKER_DATA_DIR", "ZOOM_TAKER_MODEL_CACHE_DIR",
		"ZOOM_TAKER_RECORDINGS_DIR", "ZOOM_TAKER_OUTPUT_DIR",
	} {
		t.Setenv(k, "")
	}
	cfg := Load()
	for _, c := range []struct{ got, suffix string }{
		{cfg.ModelCacheDir, "model-cache"},
		{cfg.RecordingsDir, "recordings"},
		{cfg.OutputDir, "output"},
	} {
		if !strings.HasSuffix(c.got, filepath.Join(".zoom-taker", c.suffix)) {
			t.Errorf("dir = %q, want under .zoom-taker/%s", c.got, c.suffix)
		}
	}
}

func TestBaseDirOverride(t *testing.T) {
	for _, k := range []string{
		"ZOOM_TAKER_MODEL_CACHE_DIR", "ZOOM_TAKER_RECORDINGS_DIR", "ZOOM_TAKER_OUTPUT_DIR",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("ZOOM_TAKER_DATA_DIR", "/data/zt")
	cfg := Load()
	for _, c := range []struct{ got, want string }{
		{cfg.ModelCacheDir, "/data/zt/model-cache"},
		{cfg.RecordingsDir, "/data/zt/recordings"},
		{cfg.OutputDir, "/data/zt/output"},
	} {
		if c.got != c.want {
			t.Errorf("dir = %q, want %q", c.got, c.want)
		}
	}
}

func TestPerDirOverridesBeatBase(t *testing.T) {
	t.Setenv("ZOOM_TAKER_DATA_DIR", "/data/zt")
	t.Setenv("ZOOM_TAKER_MODEL_CACHE_DIR", "")
	t.Setenv("ZOOM_TAKER_RECORDINGS_DIR", "/custom/rec")
	t.Setenv("ZOOM_TAKER_OUTPUT_DIR", "")
	cfg := Load()
	if cfg.ModelCacheDir != "/data/zt/model-cache" {
		t.Errorf("ModelCacheDir = %q", cfg.ModelCacheDir)
	}
	if cfg.RecordingsDir != "/custom/rec" {
		t.Errorf("RecordingsDir = %q, want per-dir env to win", cfg.RecordingsDir)
	}
	if cfg.OutputDir != "/data/zt/output" {
		t.Errorf("OutputDir = %q", cfg.OutputDir)
	}
}
