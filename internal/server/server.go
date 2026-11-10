// Package server ports server.py: serves the embedded recorder page, accepts
// recording uploads, and runs transcription jobs in the background. The wire
// behavior (routes, status codes, error strings, payload shapes) is kept
// compatible with the Python implementation the recorder page was built
// against; documented deviations are listed in errstrings.go.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/odg0318/zoom-taker/internal/config"
	"github.com/odg0318/zoom-taker/internal/pipeline"
	"github.com/odg0318/zoom-taker/internal/stt"
)

const (
	uploadChunk  = 64 * 1024
	maxBodyBytes = 1024 * 1024 // transcribe request cap
)

var (
	nameRe       = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
	outputFileRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+-transcript\.md$`)
	uploadExts   = []string{"webm", "caf", "m4a", "wav"}
)

// Handler implements the routes; construct with New.
type Handler struct {
	Page            []byte // embedded record-zoom.html
	MaxUploadMB     int
	RecordingsDir   string
	OutputDir       string
	DefaultLanguage string
	Model           string // model identity reported by /api/health
	Jobs            *Manager
}

// New wires a Handler from config and an engine factory.
func New(cfg config.Config, page []byte, newEngine func() (stt.Engine, error)) *Handler {
	return &Handler{
		Page:            page,
		MaxUploadMB:     cfg.MaxUploadMB,
		RecordingsDir:   cfg.RecordingsDir,
		OutputDir:       cfg.OutputDir,
		DefaultLanguage: cfg.Language,
		Model:           cfg.Model,
		Jobs:            NewManager(cfg.RecordingsDir, cfg.OutputDir, cfg.Language, newEngine),
	}
}

// Run serves until SIGINT/SIGTERM: cancels the running job, then shuts the
// listener down.
func Run(cfg config.Config, page []byte, newEngine func() (stt.Engine, error)) error {
	h := New(cfg, page, newEngine)
	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), Handler: h}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		h.Jobs.Cancel()
		_ = srv.Shutdown(context.Background())
	}()
	log.Printf("zoom-taker server on http://%s:%d (backend: whisper.cpp)", cfg.Host, cfg.Port)
	log.Printf("recordings: %s  output: %s  model cache: %s", cfg.RecordingsDir, cfg.OutputDir, cfg.ModelCacheDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorPayload{Error: msg})
}

// ServeHTTP routes manually: like BaseHTTPRequestHandler, a path registered
// for another method (or an unknown one) is simply 404.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if p := recover(); p != nil {
			writeError(w, 500, fmt.Sprint(p))
		}
	}()
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodPost:
		h.handlePost(w, r)
	default:
		writeError(w, 404, errNotFound)
	}
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	if path == "/" || path == "/record-zoom.html" {
		h.sendPage(w)
		return
	}
	if path == "/api/health" {
		writeJSON(w, 200, struct {
			Ok      bool   `json:"ok"`
			Backend string `json:"backend"`
			Model   string `json:"model"`
		}{Ok: true, Backend: "whisper.cpp", Model: h.Model})
		return
	}
	if path == "/api/status" {
		status, payload := h.Jobs.Status(r.URL.Query().Get("job"))
		writeJSON(w, status, payload)
		return
	}
	if m := outputPathRe.FindStringSubmatch(path); m != nil {
		h.handleOutputFile(w, m[1])
		return
	}
	writeError(w, 404, errNotFound)
}

var outputPathRe = regexp.MustCompile(`^/api/output/([^/]+)$`)

func (h *Handler) sendPage(w http.ResponseWriter) {
	if len(h.Page) == 0 {
		writeError(w, 500, errPageMissing)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(h.Page)))
	w.WriteHeader(200)
	_, _ = w.Write(h.Page)
}

func (h *Handler) handleOutputFile(w http.ResponseWriter, raw string) {
	filename, err := url.PathUnescape(raw)
	if err != nil {
		writeError(w, 400, errInvalidFile)
		return
	}
	if !outputFileRe.MatchString(filename) {
		writeError(w, 400, errInvalidFile)
		return
	}
	dir := filepath.Clean(h.OutputDir)
	target := filepath.Clean(filepath.Join(dir, filename))
	if filepath.Dir(target) != dir {
		writeError(w, 400, errInvalidFile)
		return
	}
	body, err := os.ReadFile(target)
	if err != nil {
		writeError(w, 404, errNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(200)
	_, _ = w.Write(body)
}

func (h *Handler) handlePost(w http.ResponseWriter, r *http.Request) {
	switch r.URL.EscapedPath() {
	case "/api/upload":
		h.handleUpload(w, r)
	case "/api/transcribe":
		h.handleTranscribe(w, r)
	default:
		writeError(w, 404, errNotFound)
	}
}

func queryValue(r *http.Request, key string) string {
	values := r.URL.Query()[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func sanitizeName(raw string) string {
	if raw == "" || !nameRe.MatchString(raw) || strings.HasPrefix(raw, ".") {
		return ""
	}
	return raw
}

func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	name := sanitizeName(queryValue(r, "name"))
	track := queryValue(r, "track")
	ext := strings.ToLower(queryValue(r, "ext"))
	if name == "" {
		writeError(w, 400, errInvalidName)
		return
	}
	if track != "remote" && track != "me" {
		writeError(w, 400, errTrack)
		return
	}
	if !uploadExtAllowed(ext) {
		writeError(w, 400, errExt)
		return
	}
	// net/http strips the Transfer-Encoding header on chunked requests and
	// reports it here instead — same condition as server.py's header check.
	if len(r.TransferEncoding) > 0 {
		w.Header().Set("Connection", "close")
		writeError(w, 501, errChunked)
		return
	}
	if r.ContentLength < 0 {
		writeError(w, 411, errLengthRequired)
		return
	}
	maxBytes := int64(h.MaxUploadMB) * 1024 * 1024
	if r.ContentLength > maxBytes {
		w.Header().Set("Connection", "close")
		writeError(w, 413, fmt.Sprintf(errTooLargeUnquoted, h.MaxUploadMB))
		return
	}

	dir := filepath.Clean(h.RecordingsDir)
	target := filepath.Clean(filepath.Join(dir, fmt.Sprintf("%s-%s.%s", name, track, ext)))
	if filepath.Dir(target) != dir {
		writeError(w, 400, errInvalidFilename)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, 500, err.Error())
		return
	}

	f, err := os.Create(target)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	written, copyErr := io.CopyBuffer(f, io.LimitReader(r.Body, r.ContentLength),
		make([]byte, uploadChunk))
	f.Close()
	if copyErr != nil || written != r.ContentLength {
		w.Header().Set("Connection", "close")
		writeError(w, 400, fmt.Sprintf(errShortBody, written, r.ContentLength))
		return
	}
	writeJSON(w, 200, struct {
		Saved string `json:"saved"`
		Bytes int64  `json:"bytes"`
	}{Saved: filepath.Base(dir) + "/" + filepath.Base(target), Bytes: written})
}

func uploadExtAllowed(ext string) bool {
	for _, allowed := range uploadExts {
		if ext == allowed {
			return true
		}
	}
	return false
}

func (h *Handler) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength < 0 {
		writeError(w, 411, errLengthRequired)
		return
	}
	if r.ContentLength > maxBodyBytes {
		writeError(w, 413, errBodyTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil && len(body) == 0 {
		writeError(w, 400, errBadJSON)
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, 400, errBadJSON)
		return
	}
	nameVal, namePresent := payload["name"]
	langVal, langPresent := payload["language"]
	// Python: payload.get("name", "") — absent name is "", absent language
	// falls back to the configured default; explicit null stringifies as
	// "None" and fails validation below.
	name := ""
	if namePresent {
		name = sanitizeName(pyStr(nameVal))
	}
	language := strings.ToLower(strings.TrimSpace(h.DefaultLanguage))
	if langPresent {
		language = strings.ToLower(strings.TrimSpace(pyStr(langVal)))
	}
	if name == "" {
		writeError(w, 400, errInvalidName)
		return
	}
	if _, langErr := pipeline.ParseLanguages(language); langErr != nil {
		writeError(w, 400, errBadLanguage)
		return
	}
	status, response := h.Jobs.Start(name, language)
	writeJSON(w, status, response)
}

// pyStr converts a JSON value the way Python's str() did for the fields the
// transcribe endpoint stringified: null -> "None", bool -> "True"/"False",
// numbers -> their literal text.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}
