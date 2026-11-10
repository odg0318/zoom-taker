// Package audio wraps the ffmpeg/ffprobe CLI calls the pipeline needs:
// decoding to 16kHz mono float32 for whisper.cpp, duration probing (with the
// Chrome-webm full-decode fallback), silence detection, and segment RMS.
// All commands honor context cancellation by killing the process.
package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"regexp"
	"strconv"
)

// Range is a half-open time interval in seconds.
type Range struct{ Start, End float64 }

func run(ctx context.Context, args ...string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return out.Bytes(), errBuf.Bytes(), err
}

// Decode converts any ffmpeg-readable audio file into 16kHz mono float32
// samples, the input format whisper.cpp expects.
func Decode(ctx context.Context, path string) ([]float32, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin",
		"-i", path, "-f", "f32le", "-acodec", "pcm_f32le", "-ac", "1", "-ar", "16000", "-")
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var samples []float32
	raw := make([]byte, 64*1024)
	var carry []byte
	for {
		n, rerr := stdout.Read(raw)
		if n > 0 {
			data := append(carry, raw[:n]...)
			whole := len(data) / 4
			for i := 0; i < whole; i++ {
				bits := uint32(data[4*i]) | uint32(data[4*i+1])<<8 | uint32(data[4*i+2])<<16 | uint32(data[4*i+3])<<24
				samples = append(samples, math.Float32frombits(bits))
			}
			carry = append([]byte{}, data[4*whole:]...)
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return nil, rerr
			}
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ffmpeg decode %s: %w: %s", path, err, bytes.TrimSpace(errBuf.Bytes()))
	}
	return samples, nil
}

var timeRe = regexp.MustCompile(`time=(\d+):(\d+):([\d.]+)`)

// Duration returns the audio length in seconds. Chrome's MediaRecorder omits
// duration in the webm header, so an unparseable ffprobe result falls back to
// a full decode scan taking the last progress stamp.
func Duration(path string) (float64, error) {
	out, _, err := run(context.Background(), "ffprobe", "-v", "error",
		"-show_entries", "format=duration", "-of", "csv=p=0", path)
	if err == nil {
		if v, perr := strconv.ParseFloat(string(bytes.TrimSpace(out)), 64); perr == nil {
			return v, nil
		}
	}
	// fallback: full decode, parse progress stamps from stderr
	_, stderr, ferr := run(context.Background(), "ffmpeg", "-nostdin", "-i", path, "-f", "null", "-")
	if ferr != nil && len(stderr) == 0 {
		return 0, ferr
	}
	ms := timeRe.FindAllSubmatch(stderr, -1)
	if len(ms) == 0 {
		return 0, fmt.Errorf("failed to measure duration: %s", path)
	}
	last := ms[len(ms)-1]
	h, _ := strconv.ParseFloat(string(last[1]), 64)
	m, _ := strconv.ParseFloat(string(last[2]), 64)
	s, _ := strconv.ParseFloat(string(last[3]), 64)
	return h*3600 + m*60 + s, nil
}

var (
	silenceStartRe = regexp.MustCompile(`silence_start: ([\d.]+)`)
	silenceEndRe   = regexp.MustCompile(`silence_end: ([\d.]+)`)
)

// SilenceRanges returns the silent intervals of the audio using ffmpeg's
// silencedetect filter. A trailing silence without an end event runs to EOF.
func SilenceRanges(ctx context.Context, path string, noiseDB, minDur float64) ([]Range, error) {
	_, stderr, err := run(ctx, "ffmpeg", "-nostdin", "-i", path,
		"-af", fmt.Sprintf("silencedetect=noise=%gdB:d=%g", noiseDB, minDur),
		"-f", "null", "-")
	if err != nil && ctx.Err() == nil {
		// silencedetect still writes its report on most decode failures; only
		// bail when nothing was captured
		if !bytes.Contains(stderr, []byte("silence_start")) {
			return nil, fmt.Errorf("silencedetect %s: %w", path, err)
		}
	}
	starts := silenceStartRe.FindAllStringSubmatch(string(stderr), -1)
	ends := silenceEndRe.FindAllStringSubmatch(string(stderr), -1)
	if len(starts) == 0 {
		return nil, nil
	}
	var ranges []Range
	for i, sm := range starts {
		s, _ := strconv.ParseFloat(sm[1], 64)
		var e float64
		if i < len(ends) {
			e, _ = strconv.ParseFloat(ends[i][1], 64)
		} else {
			e, err = Duration(path) // trailing silence runs to EOF
			if err != nil {
				return nil, err
			}
		}
		ranges = append(ranges, Range{Start: s, End: e})
	}
	return ranges, nil
}

// RMSDB measures the RMS level (dBFS) of [start, end) decoded to s16le mono,
// replicating the Python audioop-based filter math exactly: integer
// floor(sqrt(mean(v²))), db = 20·log10(rms/32768), −120 for digital silence.
func RMSDB(ctx context.Context, path string, start, end float64) (float64, error) {
	out, _, err := run(ctx, "ffmpeg", "-v", "error", "-nostdin", "-i", path,
		"-f", "s16le", "-acodec", "pcm_s16le", "-ac", "1",
		"-ss", fmt.Sprintf("%.3f", start), "-t", fmt.Sprintf("%.3f", end-start), "-")
	if err != nil && ctx.Err() == nil {
		return -120.0, nil // decode failure reads as silence, same as an empty result
	}
	if len(out)%2 == 1 {
		out = out[:len(out)-1] // drop trailing odd byte
	}
	n := len(out) / 2
	if n == 0 {
		return -120.0, nil
	}
	var acc int64
	for i := 0; i < n; i++ {
		v := int64(int16(uint16(out[2*i]) | uint16(out[2*i+1])<<8))
		acc += v * v
	}
	rms := int(math.Sqrt(float64(acc) / float64(n)))
	if rms == 0 {
		return -120.0, nil
	}
	return 20 * math.Log10(float64(rms)/32768.0), nil
}
