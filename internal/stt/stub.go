//go:build !whisper

package stt

import "errors"

// NewEngine is only available in builds carrying the "whisper" tag
// (`make build`). This stub keeps pure-Go builds and tests compiling.
func NewEngine(modelPath string, log func(string)) (Engine, error) {
	return nil, errors.New("whisper.cpp support not compiled in — build with 'make build'")
}
