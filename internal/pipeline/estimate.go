package pipeline

import (
	"runtime"
	"time"
)

// Rough wall-time model for the large-v3-turbo q5_0 engine: Metal (darwin)
// runs well under realtime in practice (measured ~0.25–0.5x on an M2), while
// CPU in Linux containers is slower. The estimate errs on the slow side and
// adds a fixed model-load overhead — it only feeds the "may take about"
// progress line, so round numbers beat precision.
const (
	estModelLoadSeconds = 10.0
	estMetalFactor      = 0.5
	estCPUFactor        = 1.5
)

// estimateWith computes the estimate for a given per-second factor
// (multiplied by the number of language passes).
func estimateWith(factor, audioSeconds float64, passes int) float64 {
	return estModelLoadSeconds + float64(passes)*audioSeconds*factor
}

// EstimateSeconds estimates wall time for transcribing audioSeconds of audio
// with `passes` language passes on the current platform.
func EstimateSeconds(audioSeconds float64, passes int) float64 {
	if runtime.GOOS == "darwin" {
		return estimateWith(estMetalFactor, audioSeconds, passes)
	}
	return estimateWith(estCPUFactor, audioSeconds, passes)
}

// FormatEstimate renders seconds as a compact duration ("45s", "2m50s").
func FormatEstimate(seconds float64) string {
	return time.Duration(seconds * float64(time.Second)).Round(time.Second).String()
}

// passWord pluralizes the language-pass count for log wording.
func passWord(n int) string {
	if n == 1 {
		return "pass"
	}
	return "passes"
}
