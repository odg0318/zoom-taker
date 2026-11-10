package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/odg0318/zoom-taker/internal/audio"
	"github.com/odg0318/zoom-taker/internal/stt"
)

// Track file conventions under recordings/: the suffix decides the speaker.
var trackSuffixes = []struct {
	Label    string
	Suffixes []string
}{
	{"REMOTE", []string{"system", "remote"}},
	{"ME", []string{"mic", "me"}},
}

var trackExts = []string{".caf", ".webm", ".m4a", ".wav"}

// NormalizeName reduces a user-supplied argument to the bare meeting name:
// a path is stripped to its base ("recordings/foo" → "foo"), and a full track
// filename additionally loses its extension and speaker suffix
// ("recordings/foo-remote.webm" → "foo"). A name without an audio extension
// is taken as-is, so a meeting legitimately named "…-me" survives.
func NormalizeName(raw string) string {
	name := filepath.Base(strings.TrimRight(raw, "/"))
	if ext := filepath.Ext(name); ext != "" {
		name = strings.TrimSuffix(name, ext)
		for _, t := range trackSuffixes {
			for _, suffix := range t.Suffixes {
				name = strings.TrimSuffix(name, "-"+suffix)
			}
		}
	}
	return name
}

// Options carries the directories and settings Meeting/TranscribeFile need.
type Options struct {
	RecordingsDir string
	OutputDir     string
	Language      string       // UI value: "kr", "en", "cn", or a combo like "kr+en"
	Log           func(string) // progress/drop lines; nil = discard
}

// findTrack looks up <recordingsDir>/<name>-<suffix><ext> in the fixed
// suffix-then-extension order.
func findTrack(recordingsDir, name, suffix string) string {
	for _, ext := range trackExts {
		path := filepath.Join(recordingsDir, name+"-"+suffix+ext)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	return ""
}

// transcribeOne runs the full per-track flow: duration + silence detection
// up front, one whisper pass per language (decoded once; the secondary pass
// fills regions the primary missed), then the hallucination filters.
func transcribeOne(ctx context.Context, engine stt.Engine, path string, langs []string,
	log func(string)) ([]Segment, error) {
	duration, err := audio.Duration(path)
	if err != nil {
		return nil, err
	}
	silence, err := audio.SilenceRanges(ctx, path, SilenceNoiseDB, SilenceMinDur)
	if err != nil {
		return nil, err
	}
	samples, err := audio.Decode(ctx, path)
	if err != nil {
		return nil, err
	}
	primary, err := engine.Transcribe(ctx, samples, langs[0])
	if err != nil {
		return nil, fmt.Errorf("transcribe %s: %w", filepath.Base(path), err)
	}
	segs := primary
	if len(langs) > 1 {
		secondary, err := engine.Transcribe(ctx, samples, langs[1])
		if err != nil {
			return nil, fmt.Errorf("transcribe %s: %w", filepath.Base(path), err)
		}
		segs = mergeLanguages(primary, secondary)
	}
	rms := func(p string, start, end float64) (float64, error) {
		return audio.RMSDB(ctx, p, start, end)
	}
	segs = clean(segs, path, duration, silence, rms, log)
	emit(log, fmt.Sprintf("%s: %d segments kept (audio %.1fs, silence %d ranges)",
		filepath.Base(path), len(segs), duration, len(silence)))
	return segs, nil
}

// Meeting transcribes the track files of meeting <name> under recordingsDir
// and writes outputDir/<name>-transcript.md. One missing track is fine
// (single-track transcription); both missing is an error. Returns the
// transcript path.
func Meeting(ctx context.Context, engine stt.Engine, name string, opts Options) (string, error) {
	log := opts.Log
	langs, err := ParseLanguages(opts.Language)
	if err != nil {
		return "", err
	}

	tracks := make(map[string][]Segment, 2)
	var missing []string
	for _, t := range trackSuffixes {
		path := ""
		for _, suffix := range t.Suffixes {
			if path = findTrack(opts.RecordingsDir, name, suffix); path != "" {
				break
			}
		}
		if path == "" {
			missing = append(missing, t.Label)
			continue
		}
		segs, err := transcribeOne(ctx, engine, path, langs, log)
		if err != nil {
			return "", err
		}
		tracks[t.Label] = segs
	}

	if len(tracks) == 0 {
		return "", fmt.Errorf("No track files found for '%s' — expected recordings/%s-{system|remote|mic|me}{.caf|.webm|.m4a|.wav}", name, name)
	}
	if len(missing) > 0 {
		sort.Strings(missing) // "ME" before "REMOTE", like Python's sorted set
		emit(log, fmt.Sprintf("warning: no %s track found — transcribing without it", strings.Join(missing, "/")))
	}

	merged := merge(tracks["REMOTE"], tracks["ME"])

	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return "", err
	}
	outPath := filepath.Join(opts.OutputDir, name+"-transcript.md")
	var b strings.Builder
	fmt.Fprintf(&b, "# %s transcript\n\n", name)
	for _, seg := range merged {
		fmt.Fprintf(&b, "[%s] %s: %s\n", FormatTimestamp(seg.Start), seg.Speaker, strings.TrimSpace(seg.Text))
	}
	if err := os.WriteFile(outPath, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	emit(log, fmt.Sprintf("Transcript saved: %s (%d segments)", outPath, len(merged)))
	return outPath, nil
}
