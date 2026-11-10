// Package config centralizes environment-variable configuration with
// defaults, mirroring the previous Python config module. All directories
// resolve relative to the working directory unless overridden, so the same
// binary works natively on macOS and inside Docker (absolute paths via env).
package config

import (
	"os"
	"path/filepath"
	"strconv"
)

// HF repo that ships the GGML whisper models.
const ModelRepo = "ggerganov/whisper.cpp"

// DefaultModel is the GGML model file downloaded when WHISPER_MODEL/MODEL is
// unset: whisper-large-v3-turbo quantized to q5_0 (574MB, near-lossless).
const DefaultModel = "ggml-large-v3-turbo-q5_0.bin"

type Config struct {
	Host          string
	Port          int
	ModelCacheDir string
	RecordingsDir string
	OutputDir     string
	Language      string // TRANSCRIBE_LANGUAGE or LANGUAGE; "kr"|"en"|"cn"|"kr+en"
	Model         string // WHISPER_MODEL or MODEL: GGML file name or local path
	MaxUploadMB   int
	HFEndpoint    string // optional mirror/proxy (huggingface-compatible)
	HFToken       string // optional, gated/private repos only
}

func env(key, alt, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if alt != "" {
		if v := os.Getenv(alt); v != "" {
			return v
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Load reads the environment. Canonical names (WHISPER_MODEL,
// TRANSCRIBE_LANGUAGE) win over the short compose aliases (MODEL, LANGUAGE).
// Generated files default to a single hidden base directory (.zoom-taker,
// overridable via ZOOM_TAKER_DATA_DIR); ZOOM_TAKER_MODEL_CACHE_DIR /
// ZOOM_TAKER_RECORDINGS_DIR / ZOOM_TAKER_OUTPUT_DIR override each location
// individually. HF_ENDPOINT/HF_TOKEN keep their ecosystem-standard names.
func Load() Config {
	wd, _ := os.Getwd()
	base := env("ZOOM_TAKER_DATA_DIR", "", filepath.Join(wd, ".zoom-taker"))
	return Config{
		Host:          env("HOST", "", "127.0.0.1"),
		Port:          envInt("PORT", 8899),
		ModelCacheDir: env("ZOOM_TAKER_MODEL_CACHE_DIR", "", filepath.Join(base, "model-cache")),
		RecordingsDir: env("ZOOM_TAKER_RECORDINGS_DIR", "", filepath.Join(base, "recordings")),
		OutputDir:     env("ZOOM_TAKER_OUTPUT_DIR", "", filepath.Join(base, "output")),
		Language:      env("TRANSCRIBE_LANGUAGE", "LANGUAGE", "kr"),
		Model:         env("WHISPER_MODEL", "MODEL", DefaultModel),
		MaxUploadMB:   envInt("MAX_UPLOAD_MB", 2048),
		HFEndpoint:    os.Getenv("HF_ENDPOINT"),
		HFToken:       os.Getenv("HF_TOKEN"),
	}
}
