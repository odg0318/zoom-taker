package pipeline

import (
	"fmt"
	"sort"
	"strings"

	"github.com/odg0318/zoom-taker/internal/audio"
	"github.com/odg0318/zoom-taker/internal/match"
)

// Cleaning thresholds — values and order of application mirror
// transcribe_meeting.py exactly.
const (
	SimilarityThreshold = 0.6 // mic/system text similarity above this -> bleed
	TimeWindowS         = 3.0 // bleed detection time window
	SilenceNoiseDB      = -45 // silencedetect threshold
	SilenceMinDur       = 1.5 // silences at least this long count

	SilenceOverlapMax = 0.5  // segment at least this fraction inside silence -> hallucination
	QuietDropDB       = 15.0 // this far below the track's speech peak -> hallucination
	QuietFloorDB      = -55.0
)

// silenceOverlap returns the fraction of a segment inside silence ranges
// (timestamps drifting past the clip end are clipped to the duration).
func silenceOverlap(seg Segment, duration float64, ranges []audio.Range) float64 {
	start := min(seg.Start, duration)
	end := min(seg.End, duration)
	var inter float64
	for _, r := range ranges {
		overlap := min(end, r.End) - max(start, r.Start)
		if overlap > 0 {
			inter += overlap
		}
	}
	return inter / max(end-start, 1e-6)
}

// clean drops hallucinated segments from one track's transcription, in the
// fixed order drift -> silence-overlap -> consecutive repeat -> quiet energy.
// Segments are compared against the track's own measured speech peak. log
// receives the drop(quiet ...) lines, printed after the pass like Python.
func clean(segs []Segment, path string, duration float64, silence []audio.Range,
	rms func(path string, start, end float64) (float64, error), log func(string)) []Segment {
	var kept []Segment
	var prevNorm string
	for _, seg := range segs {
		if seg.Start >= duration { // timestamp drift past the end
			continue
		}
		if silenceOverlap(seg, duration, silence) >= SilenceOverlapMax {
			continue // hallucination on silence
		}
		norm := match.Normalize(seg.Text)
		if norm != "" && norm == prevNorm { // consecutive repeat loop
			continue
		}
		kept = append(kept, seg)
		prevNorm = norm
	}
	// speech peak of the track; segment levels are measured once each
	peakDB := -120.0
	dbs := make([]float64, len(kept))
	for i, seg := range kept {
		db, err := rms(path, seg.Start, seg.End)
		if err != nil {
			db = -120.0
		}
		dbs[i] = db
		peakDB = max(peakDB, db)
	}
	var stages []string
	var survived []Segment
	for i, seg := range kept {
		db := dbs[i]
		if db < QuietFloorDB || db < peakDB-QuietDropDB {
			text := []rune(strings.TrimSpace(seg.Text))
			if len(text) > 30 {
				text = text[:30]
			}
			stages = append(stages, fmt.Sprintf("    drop(quiet %.0fdB < peak %.0fdB): %s", db, peakDB, string(text)))
			continue
		}
		survived = append(survived, seg)
	}
	for _, line := range stages {
		emit(log, line)
	}
	return survived
}

// merge attributes bleed: mic segments overlapping a system segment in both
// time and text are the speaker's own echo returning via Zoom, so they are
// dropped from ME and re-attributed to REMOTE. REMOTE is prepended first and
// the sort is stable, so equal starts list REMOTE before ME.
func merge(remote, me []Segment) []Labeled {
	out := make([]Labeled, 0, len(remote)+len(me))
	for _, seg := range remote {
		out = append(out, Labeled{Segment: seg, Speaker: "REMOTE"})
	}
	for _, seg := range me {
		norm := match.Normalize(seg.Text)
		bleed := false
		for _, r := range remote {
			if abs(seg.Start-r.Start) < TimeWindowS &&
				match.Ratio(norm, match.Normalize(r.Text)) > SimilarityThreshold {
				bleed = true
				break
			}
		}
		if !bleed {
			out = append(out, Labeled{Segment: seg, Speaker: "ME"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

func emit(log func(string), line string) {
	if log != nil {
		log(line)
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
