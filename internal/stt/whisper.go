//go:build whisper

package stt

// cgo-backed whisper.cpp engine. Library/header search paths are supplied by
// the build environment (LIBRARY_PATH / C_INCLUDE_PATH, staged by the
// Makefile into build/stage) — the binding's own #cgo directives carry the
// platform link flags (Metal/Accelerate frameworks on darwin).

import (
	"context"
	"fmt"
	"io"
	"strings"

	whisper "github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"
)

type engine struct {
	model whisper.Model
}

var _ Engine = (*engine)(nil)

// NewEngine loads the GGML model once (kept in memory for the process
// lifetime); every Transcribe call creates a fresh decoding context.
func NewEngine(modelPath string, log func(string)) (Engine, error) {
	m, err := whisper.New(modelPath)
	if err != nil {
		return nil, fmt.Errorf("load model %s: %w", modelPath, err)
	}
	if log != nil {
		log("whisper.cpp model loaded: " + modelPath)
	}
	return &engine{model: m}, nil
}

func (e *engine) Transcribe(ctx context.Context, samples []float32, lang string) ([]Segment, error) {
	c, err := e.model.NewContext()
	if err != nil {
		return nil, fmt.Errorf("new context: %w", err)
	}
	if err := c.SetLanguage(lang); err != nil {
		return nil, fmt.Errorf("language %q: %w", lang, err)
	}

	// Abort at the next encoder-window boundary when the context is cancelled.
	done := ctx.Done()
	encBegin := func() bool {
		if done == nil {
			return true
		}
		select {
		case <-done:
			return false
		default:
			return true
		}
	}

	if err := c.Process(samples, encBegin, nil, nil); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("transcribe: %w", err)
	}

	var out []Segment
	for {
		seg, err := c.NextSegment()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("segment: %w", err)
		}
		out = append(out, Segment{
			Start: seg.Start.Seconds(),
			End:   seg.End.Seconds(),
			Text:  strings.TrimSpace(seg.Text),
		})
	}
	return out, nil
}

func (e *engine) Close() error {
	return e.model.Close()
}
