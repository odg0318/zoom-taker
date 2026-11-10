package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odg0318/zoom-taker/internal/audio"
	"github.com/odg0318/zoom-taker/internal/stt"
)

// TranscribeFile transcribes one audio file without speaker labeling
// (the transcriber.py single-file mode): language passes merged, no
// cleaning, written to outputDir/<stem>-transcript.md. A bare filename that
// only exists in recordingsDir resolves there automatically.
func TranscribeFile(ctx context.Context, engine stt.Engine, path, language string,
	recordingsDir, outputDir string, log func(string)) (string, error) {
	langs, err := ParseLanguages(language)
	if err != nil {
		return "", err
	}
	if !exists(path) {
		inRecordings := filepath.Join(recordingsDir, filepath.Base(path))
		if exists(inRecordings) {
			path = inRecordings
		} else {
			return "", fmt.Errorf("File not found: %s", path)
		}
	}

	samples, err := audio.Decode(ctx, path)
	if err != nil {
		return "", err
	}
	primary, err := engine.Transcribe(ctx, samples, langs[0])
	if err != nil {
		return "", fmt.Errorf("transcribe %s: %w", filepath.Base(path), err)
	}
	segs := primary
	if len(langs) > 1 {
		secondary, err := engine.Transcribe(ctx, samples, langs[1])
		if err != nil {
			return "", fmt.Errorf("transcribe %s: %w", filepath.Base(path), err)
		}
		segs = mergeLanguages(primary, secondary)
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}
	outPath := filepath.Join(outputDir, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))+"-transcript.md")
	var b strings.Builder
	for _, seg := range segs {
		fmt.Fprintf(&b, "[%s] %s\n", FormatTimestamp(seg.Start), strings.TrimSpace(seg.Text))
	}
	if err := os.WriteFile(outPath, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	emit(log, fmt.Sprintf("Transcript saved: %s (%d segments)", outPath, len(segs)))
	return outPath, nil
}

func exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
