package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/odg0318/zoom-taker/internal/stt"
)

// stubEngine is a canned stt.Engine for job tests.
type stubEngine struct {
	mu    sync.Mutex
	calls int
	segs  []stt.Segment
}

func (s *stubEngine) Transcribe(_ context.Context, _ []float32, _ string) ([]stt.Segment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.segs, nil
}

func (s *stubEngine) Close() error { return nil }

func newTestHandler(t *testing.T, loader func() (stt.Engine, error)) (*Handler, string, string) {
	t.Helper()
	base := t.TempDir()
	recordings := filepath.Join(base, "recordings")
	output := filepath.Join(base, "output")
	if err := os.MkdirAll(recordings, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		Page:            []byte("<html>recorder</html>"),
		MaxUploadMB:     2,
		RecordingsDir:   recordings,
		OutputDir:       output,
		DefaultLanguage: "kr",
		Model:           "ggml-large-v3-turbo-q5_0.bin",
		Jobs:            NewManager(recordings, output, "kr", loader),
	}
	return h, recordings, output
}

func serve(t *testing.T, h *Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response not JSON: %q", rec.Body.String())
	}
	return m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestUploadValidation(t *testing.T) {
	h, _, _ := newTestHandler(t, nil)
	cases := []struct {
		query  string
		want   int
		errMsg string
	}{
		{"/api/upload", 400, errInvalidName},
		{"/api/upload?name=.dot", 400, errInvalidName},
		{"/api/upload?name=mtg", 400, errTrack},
		{"/api/upload?name=mtg&track=left", 400, errTrack},
		{"/api/upload?name=mtg&track=me", 400, errExt},
		{"/api/upload?name=mtg&track=me&ext=mp3", 400, errExt},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", c.query, bytes.NewReader([]byte("x")))
		rec := serve(t, h, req)
		if rec.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.query, rec.Code, c.want)
			continue
		}
		if got := decodeBody(t, rec)["error"]; got != c.errMsg {
			t.Errorf("%s: error %q, want %q", c.query, got, c.errMsg)
		}
	}
}

func TestUploadGuards(t *testing.T) {
	h, _, _ := newTestHandler(t, nil)

	// chunked
	req := httptest.NewRequest("POST", "/api/upload?name=mtg&track=me&ext=webm", bytes.NewReader([]byte("x")))
	req.TransferEncoding = []string{"chunked"}
	rec := serve(t, h, req)
	if rec.Code != 501 || decodeBody(t, rec)["error"] != errChunked {
		t.Errorf("chunked: %d %q", rec.Code, rec.Body.String())
	}

	// missing Content-Length
	req = httptest.NewRequest("POST", "/api/upload?name=mtg&track=me&ext=webm", nil)
	req.ContentLength = -1
	rec = serve(t, h, req)
	if rec.Code != 411 || decodeBody(t, rec)["error"] != errLengthRequired {
		t.Errorf("no CL: %d %q", rec.Code, rec.Body.String())
	}

	// over the size cap (MaxUploadMB=2 in tests)
	big := strings.Repeat("x", 3*1024*1024)
	req = httptest.NewRequest("POST", "/api/upload?name=mtg&track=me&ext=webm", strings.NewReader(big))
	req.ContentLength = int64(len(big))
	rec = serve(t, h, req)
	if rec.Code != 413 || decodeBody(t, rec)["error"] != "upload exceeds MAX_UPLOAD_MB=2" {
		t.Errorf("too large: %d %q", rec.Code, rec.Body.String())
	}

	// body shorter than Content-Length
	req = httptest.NewRequest("POST", "/api/upload?name=mtg&track=me&ext=webm", strings.NewReader("abc"))
	req.ContentLength = 100
	rec = serve(t, h, req)
	if rec.Code != 400 || decodeBody(t, rec)["error"] != "body shorter than Content-Length (3/100)" {
		t.Errorf("short body: %d %q", rec.Code, rec.Body.String())
	}
}

func TestUploadHappyPath(t *testing.T) {
	h, recordings, _ := newTestHandler(t, nil)
	payload := bytes.Repeat([]byte("RIFFdata"), 1000)
	req := httptest.NewRequest("POST", "/api/upload?name=mtg&track=me&ext=WEBM", bytes.NewReader(payload))
	rec := serve(t, h, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	if m["saved"] != "recordings/mtg-me.webm" || m["bytes"] != float64(len(payload)) {
		t.Fatalf("payload = %v", m)
	}
	saved, err := os.ReadFile(filepath.Join(recordings, "mtg-me.webm"))
	if err != nil || !bytes.Equal(saved, payload) {
		t.Fatalf("saved file mismatch: %v", err)
	}
}

func TestTranscribeValidation(t *testing.T) {
	h, _, _ := newTestHandler(t, func() (stt.Engine, error) {
		return nil, fmt.Errorf("no model in tests")
	})
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/transcribe", strings.NewReader(body))
		req.ContentLength = int64(len(body))
		return serve(t, h, req)
	}
	cases := []struct {
		body   string
		status int
		errMsg string
	}{
		{`{`, 400, errBadJSON},
		{`{"language": "kr"}`, 400, errInvalidName},
		{`{"name": ".hidden"}`, 400, errInvalidName},
		{`{"name": "mtg", "language": "fr"}`, 400, errBadLanguage},
		{`{"name": "mtg", "language": null}`, 400, errBadLanguage}, // str(None) == "None"
		{`[1,2]`, 400, errBadJSON},                                 // non-object bodies are rejected (Python surfaced a 500 here)
	}
	for _, c := range cases {
		rec := post(c.body)
		if rec.Code != c.status || decodeBody(t, rec)["error"] != c.errMsg {
			t.Errorf("%s: %d %q", c.body, rec.Code, rec.Body.String())
		}
	}
	// Python quirk kept faithful: a null name stringifies to "None", passes
	// the name regex, and starts a job — not a 400.
	rec := post(`{"name": null}`)
	if rec.Code != 202 {
		t.Errorf(`{"name": null}: %d %q, want 202 (job "None-…")`, rec.Code, rec.Body.String())
	}
	waitFor(t, "stray job to finish", func() bool {
		_, p := h.Jobs.Status("")
		return len(p.(struct {
			Jobs []jobPayload `json:"jobs"`
		}).Jobs) > 0
	})
	// oversized body (Content-Length decides, body not read)
	req := httptest.NewRequest("POST", "/api/transcribe", strings.NewReader(strings.Repeat("x", maxBodyBytes+1)))
	req.ContentLength = maxBodyBytes + 1
	rec = serve(t, h, req)
	if rec.Code != 413 || decodeBody(t, rec)["error"] != errBodyTooLarge {
		t.Errorf("too large: %d %q", rec.Code, rec.Body.String())
	}
}

func TestTranscribeStartsJob(t *testing.T) {
	h, _, _ := newTestHandler(t, func() (stt.Engine, error) {
		return nil, fmt.Errorf("no model in tests")
	})
	body := `{"name": "mtg", "language": "Kr+EN"}`
	req := httptest.NewRequest("POST", "/api/transcribe", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := serve(t, h, req)
	if rec.Code != 202 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	if m["state"] != "queued" {
		t.Fatalf("payload = %v", m)
	}
	jobID, _ := m["job"].(string)
	if !strings.HasPrefix(jobID, "mtg-") {
		t.Fatalf("job id = %q, want mtg-<epoch>", jobID)
	}
	// the queued job fails fast (engine loader error); wait, then the slot frees
	waitFor(t, "job to finish", func() bool {
		_, p := h.Jobs.Status(jobID)
		payload := p.(jobPayload)
		return payload.State == "error" && payload.ReturnCode != nil && *payload.ReturnCode == 1
	})
	_, p := h.Jobs.Status(jobID)
	payload := p.(jobPayload)
	logs := strings.Join(payload.Log, "\n")
	if !strings.Contains(logs, "server error: no model in tests") ||
		!strings.Contains(logs, "transcription exited with code 1") {
		t.Fatalf("job log = %v", payload.Log)
	}
}

func TestTranscribeBusy(t *testing.T) {
	release := make(chan struct{})
	h, _, _ := newTestHandler(t, func() (stt.Engine, error) {
		<-release
		return nil, fmt.Errorf("cancelled")
	})
	start := func(name string) (*httptest.ResponseRecorder, map[string]any) {
		body := fmt.Sprintf(`{"name": %q}`, name)
		req := httptest.NewRequest("POST", "/api/transcribe", strings.NewReader(body))
		req.ContentLength = int64(len(body))
		rec := serve(t, h, req)
		return rec, decodeBody(t, rec)
	}
	rec1, m1 := start("first")
	if rec1.Code != 202 {
		t.Fatalf("first start: %d %v", rec1.Code, m1)
	}
	firstID := m1["job"].(string)
	waitFor(t, "job to reach running", func() bool {
		_, p := h.Jobs.Status(firstID)
		return p.(jobPayload).State == "running"
	})

	rec2, m2 := start("second")
	if rec2.Code != 409 {
		t.Fatalf("busy start: %d %v", rec2.Code, m2)
	}
	if m2["error"] != errBusy || m2["job"] != firstID {
		t.Fatalf("busy payload = %v", m2)
	}

	close(release)
	waitFor(t, "slot to free", func() bool {
		_, p := h.Jobs.Status(firstID)
		return p.(jobPayload).State == "error"
	})
	rec3, _ := start("third")
	if rec3.Code != 202 {
		t.Fatalf("third start after free: %d", rec3.Code)
	}
}

func TestStatusUnknownJob(t *testing.T) {
	h, _, _ := newTestHandler(t, nil)
	req := httptest.NewRequest("GET", "/api/status?job=ghost", nil)
	rec := serve(t, h, req)
	if rec.Code != 404 || decodeBody(t, rec)["error"] != errUnknownJob {
		t.Fatalf("status: %d %q", rec.Code, rec.Body.String())
	}
	// empty job value lists instead
	req = httptest.NewRequest("GET", "/api/status?job=", nil)
	rec = serve(t, h, req)
	if rec.Code != 200 {
		t.Fatalf("list: %d", rec.Code)
	}
	var list struct {
		Jobs []jobPayload `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Jobs == nil {
		t.Fatalf("list body = %q (%v)", rec.Body.String(), err)
	}
}

func TestStatusOrderNewestFirst(t *testing.T) {
	h, _, _ := newTestHandler(t, func() (stt.Engine, error) {
		return nil, fmt.Errorf("no model")
	})
	base := time.Now().Add(-100 * time.Hour)
	for i := 0; i < 3; i++ {
		i := i
		waitFor(t, "slot free", func() bool {
			h.Jobs.mu.Lock()
			defer h.Jobs.mu.Unlock()
			return h.Jobs.active == ""
		})
		h.Jobs.mu.Lock()
		h.Jobs.now = func() time.Time { return base.Add(time.Duration(i) * time.Second) }
		h.Jobs.mu.Unlock()
		body := fmt.Sprintf(`{"name": "m%d"}`, i)
		req := httptest.NewRequest("POST", "/api/transcribe", strings.NewReader(body))
		req.ContentLength = int64(len(body))
		serve(t, h, req)
	}
	waitFor(t, "jobs to finish", func() bool {
		_, p := h.Jobs.Status("")
		jobs := p.(struct {
			Jobs []jobPayload `json:"jobs"`
		}).Jobs
		return len(jobs) == 3
	})
	_, p := h.Jobs.Status("")
	jobs := p.(struct {
		Jobs []jobPayload `json:"jobs"`
	}).Jobs
	for i, j := range jobs {
		want := fmt.Sprintf("m%d", 2-i) // newest first
		if !strings.HasPrefix(j.Job, want+"-") {
			t.Fatalf("jobs[%d] = %s, want prefix %s", i, j.Job, want)
		}
	}
}

func TestManagerRingTruncation(t *testing.T) {
	m := NewManager("", "", "kr", nil)
	j := &Job{Log: []string{}}
	m.mu.Lock()
	for i := 0; i < ringLines+60; i++ {
		m.appendLogLocked(j, fmt.Sprintf("line %d", i))
	}
	p := payloadOf(j)
	m.mu.Unlock()
	if len(p.Log) != displayLines {
		t.Fatalf("payload log = %d lines, want %d", len(p.Log), displayLines)
	}
	if p.Log[0] != fmt.Sprintf("line %d", ringLines+60-displayLines) {
		t.Fatalf("first kept line = %q", p.Log[0])
	}
	if len(j.Log) != ringLines {
		t.Fatalf("ring = %d lines, want %d", len(j.Log), ringLines)
	}
}

func TestEvictionKeepsTwentyNewest(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir(), "kr", func() (stt.Engine, error) {
		return nil, fmt.Errorf("no model")
	})
	base := time.Now().Add(-1000 * time.Hour)
	for i := 0; i < 25; i++ {
		// the single-job slot must be free before starting the next one
		waitFor(t, fmt.Sprintf("slot free before job %d", i), func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.active == ""
		})
		m.mu.Lock()
		m.now = func() time.Time { return base.Add(time.Duration(i) * time.Second) }
		m.mu.Unlock()
		if status, _ := m.Start("mtg", "kr"); status != 202 {
			t.Fatalf("start %d: %d", i, status)
		}
	}
	waitFor(t, "all jobs to finish", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.active == ""
	})
	_, p := m.Status("")
	jobs := p.(struct {
		Jobs []jobPayload `json:"jobs"`
	}).Jobs
	if len(jobs) != maxJobs {
		t.Fatalf("kept %d jobs, want %d", len(jobs), maxJobs)
	}
	// lexicographic-largest ids survive: mtg-<epoch> with growing epochs
	newest := jobs[0].Job
	want := fmt.Sprintf("mtg-%d", base.Add(24*time.Second).Unix())
	if newest != want {
		t.Fatalf("newest = %s, want %s", newest, want)
	}
}

func TestOutputFileRouting(t *testing.T) {
	h, _, output := newTestHandler(t, nil)
	if err := os.WriteFile(filepath.Join(output, "mtg-transcript.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		return serve(t, h, httptest.NewRequest("GET", path, nil))
	}
	rec := get("/api/output/mtg-transcript.md")
	if rec.Code != 200 || rec.Body.String() != "# hi" {
		t.Fatalf("happy path: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
		t.Fatalf("content type = %q", ct)
	}
	for _, path := range []string{
		"/api/output/../mtg-transcript.md",     // traversal
		"/api/output/%2e%2e/mtg-transcript.md", // encoded traversal
		"/api/output/mtg-transcript.txt",       // wrong extension
		"/api/output/ghost-transcript.md",      // missing
	} {
		rec := get(path)
		if rec.Code == 200 {
			t.Errorf("%s: expected non-200, got %d", path, rec.Code)
		}
	}
	// percent-encoded but valid name decodes before the regex
	rec = get("/api/output/mtg-transcript%2Emd")
	if rec.Code != 200 {
		t.Errorf("encoded name: %d %q", rec.Code, rec.Body.String())
	}
}

func TestPageAndHealth(t *testing.T) {
	h, _, _ := newTestHandler(t, nil)
	rec := serve(t, h, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || rec.Body.String() != "<html>recorder</html>" {
		t.Fatalf("page: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("page content type = %q", ct)
	}
	rec = serve(t, h, httptest.NewRequest("GET", "/record-zoom.html", nil))
	if rec.Code != 200 {
		t.Fatalf("alt page path: %d", rec.Code)
	}
	rec = serve(t, h, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != 200 {
		t.Fatalf("health: %d", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["ok"] != true || m["backend"] != "whisper.cpp" || m["model"] != h.Model {
		t.Fatalf("health = %v", m)
	}
	// unknown routes 404 in both methods
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/nope"}, {"POST", "/api/nope"}, {"GET", "/api/upload"},
	} {
		req := httptest.NewRequest(r.method, r.path, nil)
		if rec := serve(t, h, req); rec.Code != 404 {
			t.Errorf("%s %s: %d, want 404", r.method, r.path, rec.Code)
		}
	}
}

func TestJobLifecycleDone(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	h, recordings, _ := newTestHandler(t, nil)
	fixture, err := os.ReadFile(filepath.Join("..", "pipeline", "testdata", "speech.wav"))
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"system", "mic"} {
		if err := os.WriteFile(filepath.Join(recordings, "done-"+suffix+".wav"), fixture, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	engine := &stubEngine{segs: []stt.Segment{{Start: 0, End: 2, Text: "테스트 발언"}}}
	h.Jobs.newEngine = func() (stt.Engine, error) { return engine, nil }

	body := `{"name": "done", "language": "kr"}`
	req := httptest.NewRequest("POST", "/api/transcribe", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := serve(t, h, req)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	jobID := decodeBody(t, rec)["job"].(string)
	waitFor(t, "job done", func() bool {
		_, p := h.Jobs.Status(jobID)
		return p.(jobPayload).State == "done"
	})
	_, p := h.Jobs.Status(jobID)
	payload := p.(jobPayload)
	if payload.Transcript == nil || *payload.Transcript != "/api/output/done-transcript.md" {
		t.Fatalf("transcript link = %v", payload.Transcript)
	}
	if payload.ReturnCode == nil || *payload.ReturnCode != 0 {
		t.Fatalf("returncode = %v", payload.ReturnCode)
	}
	for _, frag := range []string{
		"done-system.wav: 1 segments kept (audio 2.0s, silence 0 ranges)",
		"done-mic.wav: 1 segments kept (audio 2.0s, silence 0 ranges)",
	} {
		found := false
		for _, line := range payload.Log {
			if strings.Contains(line, frag) {
				found = true
			}
		}
		if !found {
			t.Fatalf("log missing %q: %v", frag, payload.Log)
		}
	}
	// the transcript link actually serves
	req = httptest.NewRequest("GET", *payload.Transcript, nil)
	rec = serve(t, h, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "REMOTE") {
		t.Fatalf("transcript fetch: %d %q", rec.Code, rec.Body.String())
	}
}
