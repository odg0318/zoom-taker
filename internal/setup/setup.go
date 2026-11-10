// Package setup implements the first-run wizard: create the data
// directories and fetch the Whisper model when it is not cached yet.
// It runs on every serve start (idempotent); the download prompt only
// fires in interactive terminals — pipes/CI/Docker download automatically.
package setup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/odg0318/zoom-taker/internal/config"
	"github.com/odg0318/zoom-taker/internal/modelcache"
)

// Ensure creates the data directories under the configured base and makes
// the configured model available, prompting before a download when
// interactive is true (in must be the stdin to read the answer from).
// A declined or failed download is not an error — recording keeps working
// and transcription jobs surface the model error on use.
func Ensure(ctx context.Context, cfg config.Config, in io.Reader, interactive bool, log func(string)) error {
	dirs := []struct{ label, path string }{
		{"model cache", cfg.ModelCacheDir},
		{"recordings", cfg.RecordingsDir},
		{"output", cfg.OutputDir},
	}
	var created int
	for _, d := range dirs {
		if _, err := os.Stat(d.path); err == nil {
			continue
		}
		if err := os.MkdirAll(d.path, 0o755); err != nil {
			return fmt.Errorf("setup: create %s dir: %w", d.label, err)
		}
		emit(log, fmt.Sprintf("setup: created %s dir %s", d.label, d.path))
		created++
	}
	if created == 0 {
		emit(log, fmt.Sprintf("setup: data dirs under %s ready", cfg.ModelCacheDir))
	}

	// A path model is used verbatim by Resolve — nothing to fetch here.
	if strings.ContainsRune(cfg.Model, '/') || strings.ContainsRune(cfg.Model, filepath.Separator) {
		if st, err := os.Stat(cfg.Model); err == nil && !st.IsDir() {
			emit(log, fmt.Sprintf("setup: model file %s (%dMiB)", cfg.Model, st.Size()/modelcache.MiB))
			return nil
		}
		emit(log, fmt.Sprintf("setup: warning: model file not found: %s — transcription will fail", cfg.Model))
		return nil
	}

	if path := modelcache.CachedPath(cfg.Model, cfg.ModelCacheDir, log); path != "" {
		emit(log, fmt.Sprintf("setup: model cached: %s", path))
		return nil
	}

	emit(log, fmt.Sprintf("setup: model %s is not in %s", cfg.Model, cfg.ModelCacheDir))
	if interactive {
		emit(log, fmt.Sprintf("download %s now? [Y/n]", cfg.Model))
		if !confirm(in) {
			emit(log, "setup: skipped — run `zoom-taker download` (or restart) when ready")
			return nil
		}
	} else {
		emit(log, "setup: non-interactive session — downloading automatically")
	}
	path, err := modelcache.Resolve(ctx, cfg.Model, cfg.ModelCacheDir, cfg.HFEndpoint, cfg.HFToken, log)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	emit(log, fmt.Sprintf("setup: model ready: %s", path))
	return nil
}

// confirm reads a yes/no line, defaulting to yes on empty input or EOF.
func confirm(in io.Reader) bool {
	if in == nil {
		return true
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if line == "" && err != nil {
		return true // EOF (e.g. Ctrl-D): take the default
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	default:
		return false
	}
}

func emit(log func(string), line string) {
	if log != nil {
		log(line)
	}
}
