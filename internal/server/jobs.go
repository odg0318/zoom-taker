package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/odg0318/zoom-taker/internal/pipeline"
	"github.com/odg0318/zoom-taker/internal/stt"
)

const (
	ringLines    = 500 // job log ring size
	displayLines = 50  // lines shown in the status payload
	maxJobs      = 20  // remembered jobs (lexicographic-largest ids win)
)

// Job mirrors server.py's dataclass: queued | running | done | error.
type Job struct {
	ID         string
	Name       string
	State      string
	Log        []string // ring, oldest first
	ReturnCode *int     // null until the job finishes
	Transcript *string  // null until done
}

// jobPayload is the /api/status wire shape (field order matches the Python
// dict for byte-level familiarity, though clients parse JSON anyway).
type jobPayload struct {
	Job        string   `json:"job"`
	Name       string   `json:"name"`
	State      string   `json:"state"`
	Log        []string `json:"log"`
	ReturnCode *int     `json:"returncode"`
	Transcript *string  `json:"transcript"`
}

// Manager runs at most one transcription at a time, in-process. newEngine
// supplies the STT engine (a fresh subprocess load per job in Python; the Go
// wiring loads the model lazily and reuses it process-wide).
type Manager struct {
	mu              sync.Mutex
	jobs            map[string]*Job
	active          string
	runningCancel   context.CancelFunc
	now             func() time.Time // injectable for tests
	recordingsDir   string
	outputDir       string
	defaultLanguage string
	newEngine       func() (stt.Engine, error)
}

func NewManager(recordingsDir, outputDir, defaultLanguage string,
	newEngine func() (stt.Engine, error)) *Manager {
	return &Manager{
		jobs:            make(map[string]*Job),
		now:             time.Now,
		recordingsDir:   recordingsDir,
		outputDir:       outputDir,
		defaultLanguage: defaultLanguage,
		newEngine:       newEngine,
	}
}

// payloadOf builds the status view of a job; caller holds mu.
func payloadOf(j *Job) jobPayload {
	logs := j.Log
	if len(logs) > displayLines {
		logs = logs[len(logs)-displayLines:]
	}
	out := make([]string, len(logs))
	copy(out, logs)
	return jobPayload{
		Job: j.ID, Name: j.Name, State: j.State,
		Log: out, ReturnCode: j.ReturnCode, Transcript: j.Transcript,
	}
}

// Start queues a transcription. Only one may run at a time; the second
// request gets 409 with the active job id.
func (m *Manager) Start(name, language string) (int, any) {
	m.mu.Lock()
	if m.active != "" {
		if j, ok := m.jobs[m.active]; ok && (j.State == "queued" || j.State == "running") {
			id := m.active
			m.mu.Unlock()
			return 409, busyPayload{Error: errBusy, Job: id}
		}
	}
	j := &Job{
		ID:    fmt.Sprintf("%s-%d", name, m.now().Unix()),
		Name:  name,
		State: "queued",
		Log:   []string{},
	}
	m.jobs[j.ID] = j
	ids := make([]string, 0, len(m.jobs))
	for id := range m.jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, old := range ids[:max(0, len(ids)-maxJobs)] {
		delete(m.jobs, old)
	}
	m.active = j.ID
	state := j.State // captured under the lock; run() flips it to "running"
	m.mu.Unlock()
	go m.run(j, language)
	return 202, struct {
		Job   string `json:"job"`
		State string `json:"state"`
	}{Job: j.ID, State: state}
}

// run executes the meeting pipeline in this goroutine, streaming log lines
// into the job's ring. Mirrors run_job(): child failures become
// "transcription exited with code 1", server-side failures "server error: ...".
func (m *Manager) run(j *Job, language string) {
	m.mu.Lock()
	j.State = "running"
	ctx, cancel := context.WithCancel(context.Background())
	m.runningCancel = cancel
	logLine := func(line string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.appendLogLocked(j, line)
	}
	m.mu.Unlock()

	var engine stt.Engine
	var err error
	if m.newEngine == nil {
		err = errors.New("no engine loader configured")
	} else {
		engine, err = m.newEngine()
	}
	if err == nil {
		_, err = pipeline.Meeting(ctx, engine, j.Name, pipeline.Options{
			RecordingsDir: m.recordingsDir,
			OutputDir:     m.outputDir,
			Language:      language,
			Log:           logLine,
		})
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.runningCancel = nil
	cancel()
	if m.active == j.ID {
		m.active = ""
	}
	switch {
	case err != nil:
		// pipeline failure == Python child exiting nonzero
		if engine != nil {
			m.appendLogLocked(j, err.Error())
		} else {
			// engine loading is a server-side failure, like a thread exception
			m.appendLogLocked(j, "server error: "+err.Error())
		}
		j.State = "error"
		rc := 1
		j.ReturnCode = &rc
		m.appendLogLocked(j, fmt.Sprintf("transcription exited with code %d", rc))
	default:
		target := filepath.Join(m.outputDir, j.Name+"-transcript.md")
		link := "/api/output/" + j.Name + "-transcript.md"
		if _, statErr := os.Stat(target); statErr == nil {
			j.State = "done"
			rc := 0
			j.ReturnCode = &rc
			j.Transcript = &link
			return
		}
		// pipeline succeeded but wrote nothing — error without an exit line,
		// same as Python's rc==0-and-missing-file branch
		j.State = "error"
		rc := 0
		j.ReturnCode = &rc
	}
}

// appendLogLocked adds a line to the job's ring; caller holds mu.
func (m *Manager) appendLogLocked(j *Job, line string) {
	j.Log = append(j.Log, line)
	if len(j.Log) > ringLines {
		j.Log = j.Log[len(j.Log)-ringLines:]
	}
}

// Status serves /api/status: one job when the query names it, otherwise all
// remembered jobs, newest (lexicographically largest) id first.
func (m *Manager) Status(jobID string) (int, any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if jobID != "" {
		j, ok := m.jobs[jobID]
		if !ok {
			return 404, errorPayload{Error: errUnknownJob}
		}
		return 200, payloadOf(j)
	}
	ids := make([]string, 0, len(m.jobs))
	for id := range m.jobs {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	jobs := make([]jobPayload, 0, len(ids))
	for _, id := range ids {
		jobs = append(jobs, payloadOf(m.jobs[id]))
	}
	return 200, struct {
		Jobs []jobPayload `json:"jobs"`
	}{Jobs: jobs}
}

// Cancel stops the running job (SIGINT/SIGTERM path). The pipeline aborts at
// the next encoder window boundary, so shutdown can lag by up to ~30s.
func (m *Manager) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runningCancel != nil {
		m.runningCancel()
	}
}
