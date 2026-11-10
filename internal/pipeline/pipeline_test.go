package pipeline

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odg0318/zoom-taker/internal/audio"
)

// fakeEngine replays scripted per-call responses (one Transcribe call per
// track per language pass) and records the requested languages.
type fakeEngine struct {
	responses [][]Segment
	calls     []string
}

func (f *fakeEngine) Transcribe(_ context.Context, _ []float32, lang string) ([]Segment, error) {
	f.calls = append(f.calls, lang)
	i := len(f.calls) - 1
	if i < len(f.responses) {
		return f.responses[i], nil
	}
	return nil, nil
}

func (f *fakeEngine) Close() error { return nil }

var compositePath string // silence(2s) + speech(2s) + silence(2s) + quiet(1.2s) = 7.2s

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "pipeline-test")
	if err != nil {
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	compositePath = filepath.Join(dir, "composite.wav")
	gen := func(args ...string) error {
		return exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).Run()
	}
	td := func(name string) string { return filepath.Join("testdata", name) }
	// [0,2)silence [2,4)speech [4,6)silence [6,7.2)quiet — quiet.wav is sparse
	// clicks (peak -40dB stays above the -45dB silencedetect threshold, 1% duty
	// cycle puts RMS at -63dB below the floor), so it reaches the energy filter
	// like real mic bleed does; trimmed to 1.2s to keep the tail past EOF drift.
	err = gen("-i", td("silence.wav"), "-i", td("speech.wav"), "-i", td("silence.wav"),
		"-i", td("quiet.wav"),
		"-filter_complex", "[3:a]atrim=duration=1.2[q];[0:a][1:a][2:a][q]concat=n=4:v=0:a=1[out]",
		"-map", "[out]", compositePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "composite gen:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func copyTo(dst, src string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func TestParseLanguages(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"kr", []string{"ko"}},
		{"en", []string{"en"}},
		{"cn", []string{"zh"}},
		{"KR", []string{"ko"}},   // lowercased
		{" kr ", []string{"ko"}}, // trimmed
		{"kr+en", []string{"ko", "en"}},
		{"en+en", []string{"en"}}, // dedup collapses to one pass
	}
	for _, c := range cases {
		got, err := ParseLanguages(c.in)
		if err != nil {
			t.Errorf("ParseLanguages(%q) error: %v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("ParseLanguages(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParseLanguages(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	}
	for _, bad := range []string{"", "fr", "kr+en+fr", "+"} {
		_, err := ParseLanguages(bad)
		want := fmt.Sprintf("Unsupported language '%s' (expected: kr, en, cn, or a two-language combo like kr+en)", bad)
		if err == nil || err.Error() != want {
			t.Errorf("ParseLanguages(%q) error = %v, want %q", bad, err, want)
		}
	}
}

func TestMergeLanguages(t *testing.T) {
	primary := []Segment{{Start: 0, End: 10, Text: "A"}}
	secondary := []Segment{
		{Start: 1, End: 3, Text: "B"},     // fully covered by primary -> dropped
		{Start: 9, End: 11, Text: "C"},    // exactly 0.5 overlap -> dropped (kept only when < 0.5)
		{Start: 10.5, End: 12, Text: "D"}, // uncovered -> fills
	}
	got := mergeLanguages(primary, secondary)
	if len(got) != 2 || got[0].Text != "A" || got[1].Text != "D" {
		t.Fatalf("mergeLanguages = %+v, want [A D]", got)
	}
}

func TestMergeBleed(t *testing.T) {
	remote := []Segment{{Start: 10, End: 12, Text: "안녕하세요"}, {Start: 5, End: 7, Text: "X"}}
	me := []Segment{
		{Start: 11.5, End: 13, Text: "안녕하세요"},     // time+text match -> bleed -> REMOTE
		{Start: 13, End: 15, Text: "안녕하세요"},       // |13-10| = 3.0, not < 3.0 -> ME
		{Start: 5, End: 7, Text: "Y"},             // ratio("X","Y") low -> ME; tie keeps REMOTE first
		{Start: 20, End: 22, Text: "hello world"}, // ratio 0.545 vs "X" below threshold -> ME
	}
	got := merge(remote, me)
	// expected in final (stable-sorted) order: the start-5 tie lists REMOTE first
	wantStarts := []float64{5, 5, 10, 13, 20}
	wantSpeakers := []string{"REMOTE", "ME", "REMOTE", "ME", "ME"}
	if len(got) != len(wantStarts) {
		t.Fatalf("merge = %d segs, want %d", len(got), len(wantStarts))
	}
	for i := range wantStarts {
		if got[i].Speaker != wantSpeakers[i] || got[i].Start != wantStarts[i] {
			t.Errorf("merge[%d] = %s@%v, want %s@%v", i, got[i].Speaker, got[i].Start, wantSpeakers[i], wantStarts[i])
		}
	}
}

func TestCleanFilters(t *testing.T) {
	ctx := context.Background()
	if compositePath == "" {
		t.Skip("fixtures unavailable")
	}
	duration, err := audio.Duration(compositePath)
	if err != nil {
		t.Fatal(err)
	}
	if duration < 7.0 || duration > 7.5 {
		t.Fatalf("composite duration = %v, want ~7.2", duration)
	}
	silence, err := audio.SilenceRanges(ctx, compositePath, SilenceNoiseDB, SilenceMinDur)
	if err != nil {
		t.Fatal(err)
	}
	if len(silence) != 2 { // [0,2) and [4,6); the quiet tail escapes (1.2s < 1.5s min)
		t.Fatalf("silence ranges = %+v, want 2", silence)
	}

	segs := []Segment{
		{Start: 0.5, End: 1.5, Text: "침묵 구간"}, // 100% inside silence
		{Start: 2, End: 4, Text: "스피치 문장"},    // kept, defines the peak
		{Start: 2, End: 4, Text: "스피치 문장."},   // repeat after normalize
		{Start: 2.5, End: 3.5, Text: "..."},   // empty norm: never a repeat, kept
		{Start: 2.7, End: 3.7, Text: "!!!"},   // empty norm reset again, kept
		{Start: 6, End: 7.2, Text: "조용한 문장"},  // escapes silence, below -55dB floor
		{Start: 7.5, End: 8, Text: "드리프트"},    // starts past the audio end
	}
	var lines []string
	rms := func(path string, start, end float64) (float64, error) {
		return audio.RMSDB(ctx, path, start, end)
	}
	got := clean(segs, compositePath, duration, silence, rms, func(s string) { lines = append(lines, s) })

	if len(got) != 3 || got[0].Text != "스피치 문장" || got[1].Text != "..." || got[2].Text != "!!!" {
		t.Fatalf("clean kept = %+v, want [스피치 문장 ... !!!]", got)
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "    drop(quiet -") || !strings.Contains(lines[0], "dB < peak -") || !strings.HasSuffix(lines[0], ": 조용한 문장") {
		t.Fatalf("drop log = %+v, want one drop(quiet ...) line", lines)
	}
}

func TestMeeting(t *testing.T) {
	ctx := context.Background()
	if compositePath == "" {
		t.Skip("fixtures unavailable")
	}
	base := t.TempDir()
	recordings := filepath.Join(base, "recordings")
	output := filepath.Join(base, "output")
	if err := os.MkdirAll(recordings, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyTo(filepath.Join(recordings, "mtg-system.wav"), filepath.Join("testdata", "speech.wav")); err != nil {
		t.Fatal(err)
	}
	if err := copyTo(filepath.Join(recordings, "mtg-mic.wav"), filepath.Join("testdata", "speech.wav")); err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{responses: [][]Segment{
		{{Start: 0, End: 2, Text: "원격 발언입니다"}},                                   // REMOTE track
		{{Start: 0, End: 2, Text: "원격 발언입니다"}, {Start: 1, End: 2, Text: "내 발언"}}, // ME track: first is bleed
	}}
	var lines []string
	out, err := Meeting(ctx, engine, "mtg", Options{
		RecordingsDir: recordings, OutputDir: output, Language: "kr",
		Log: func(s string) { lines = append(lines, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if engine.calls[0] != "ko" || engine.calls[1] != "ko" {
		t.Fatalf("engine calls = %v, want two ko passes", engine.calls)
	}
	want := "# mtg transcript\n\n[00:00] REMOTE: 원격 발언입니다\n[00:01] ME: 내 발언\n"
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Fatalf("transcript =\n%q\nwant\n%q", string(raw), want)
	}
	joined := strings.Join(lines, "\n")
	for _, frag := range []string{
		"mtg-system.wav: 1 segments kept (audio 2.0s, silence 0 ranges)",
		"mtg-mic.wav: 2 segments kept (audio 2.0s, silence 0 ranges)",
		"Transcript saved: " + out + " (2 segments)",
	} {
		if !strings.Contains(joined, frag) {
			t.Errorf("log missing %q; got:\n%s", frag, joined)
		}
	}
}

func TestMeetingSingleTrack(t *testing.T) {
	ctx := context.Background()
	if compositePath == "" {
		t.Skip("fixtures unavailable")
	}
	base := t.TempDir()
	recordings := filepath.Join(base, "recordings")
	output := filepath.Join(base, "output")
	if err := os.MkdirAll(recordings, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyTo(filepath.Join(recordings, "half-mic.wav"), filepath.Join("testdata", "speech.wav")); err != nil {
		t.Fatal(err)
	}
	var lines []string
	engine := &fakeEngine{responses: [][]Segment{{{Start: 0, End: 2, Text: "혼자 말하기"}}}}
	out, err := Meeting(ctx, engine, "half", Options{
		RecordingsDir: recordings, OutputDir: output, Language: "kr",
		Log: func(s string) { lines = append(lines, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "warning: no REMOTE track found — transcribing without it") {
		t.Errorf("log missing single-track warning; got:\n%s", joined)
	}
	raw, _ := os.ReadFile(out)
	if string(raw) != "# half transcript\n\n[00:00] ME: 혼자 말하기\n" {
		t.Fatalf("transcript = %q", string(raw))
	}
}

func TestMeetingNoTracks(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	_, err := Meeting(ctx, &fakeEngine{}, "ghost", Options{
		RecordingsDir: filepath.Join(base, "recordings"),
		OutputDir:     filepath.Join(base, "output"),
		Language:      "kr",
	})
	want := "No track files found for 'ghost' — expected recordings/ghost-{system|remote|mic|me}{.caf|.webm|.m4a|.wav}"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestTranscribeFile(t *testing.T) {
	ctx := context.Background()
	if compositePath == "" {
		t.Skip("fixtures unavailable")
	}
	base := t.TempDir()
	recordings := filepath.Join(base, "recordings")
	output := filepath.Join(base, "output")
	if err := os.MkdirAll(recordings, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyTo(filepath.Join(recordings, "one.wav"), filepath.Join("testdata", "speech.wav")); err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{responses: [][]Segment{{{Start: 0.2, End: 2, Text: "한 문장"}}}}
	var lines []string
	// bare filename that only exists under recordings/ resolves there
	out, err := TranscribeFile(ctx, engine, "one.wav", "kr", recordings, output,
		func(s string) { lines = append(lines, s) })
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	if string(raw) != "[00:00] 한 문장\n" {
		t.Fatalf("transcript = %q, want single unlabeled line", string(raw))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Transcript saved: "+out+" (1 segments)") {
		t.Fatalf("log = %v", lines)
	}
	// unknown file
	_, err = TranscribeFile(ctx, engine, "nope.wav", "kr", recordings, output, nil)
	if err == nil || err.Error() != "File not found: nope.wav" {
		t.Fatalf("error = %v, want file-not-found", err)
	}
}
