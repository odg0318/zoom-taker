// Package stt defines the speech-to-text engine interface used by the
// transcription pipeline. The cgo-backed whisper.cpp implementation lives in
// whisper.go behind the "whisper" build tag; stub.go (default) keeps
// `go build ./...` working without the C toolchain.
package stt

import "context"

// Segment mirrors the pipeline's segment shape: seconds since track start.
type Segment struct {
	Start, End float64
	Text       string
}

// Engine is a loaded STT model. One engine instance serves every pass;
// implementations must be safe for sequential (not concurrent) use.
type Engine interface {
	// Transcribe runs one decoding pass over 16kHz mono samples.
	// lang is a Whisper language code ("ko", "en", "zh").
	Transcribe(ctx context.Context, samples []float32, lang string) ([]Segment, error)
	Close() error
}
