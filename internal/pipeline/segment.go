package pipeline

import (
	"fmt"
	"sort"

	"github.com/odg0318/zoom-taker/internal/stt"
)

// Segment is the transcription unit shared with the stt engine.
type Segment = stt.Segment

// Labeled is a segment attributed to a speaker in the merged transcript.
type Labeled struct {
	Segment
	Speaker string // "ME" | "REMOTE"
}

// SortSegments orders segments by start time. Stable, so equal starts keep
// insertion order — merge() relies on REMOTE coming before ME on ties.
func SortSegments(segs []Segment) {
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
}

// FormatTimestamp renders seconds as MM:SS with the minute truncated
// (f"{int(t) // 60:02d}:{int(t) % 60:02d}").
func FormatTimestamp(t float64) string {
	s := int(t)
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}
