// Package pipeline ports transcribe_meeting.py + the language-pass logic of
// transcriber.py: per-track whisper transcription, hallucination cleaning,
// ME/REMOTE bleed attribution, dual-language merging, and the markdown
// transcript writer.
package pipeline

import (
	"fmt"
	"strings"
)

// UI/env-facing language codes -> whisper ISO-639-1 codes.
var languageCodes = map[string]string{"kr": "ko", "en": "en", "cn": "zh"}

// LangMergeOverlap: a secondary-language segment overlapping a primary one
// this much is discarded (the primary pass owns that time window).
const LangMergeOverlap = 0.5

// ParseLanguages converts a UI language value into whisper codes, primary
// pass first: "kr" -> ["ko"], "kr+en" -> ["ko", "en"]. A combined value runs
// one pass per language; the secondary fills regions the primary missed.
func ParseLanguages(lang string) ([]string, error) {
	var parts []string
	for _, p := range strings.Split(lang, "+") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 2 && parts[0] == parts[1] {
		parts = parts[:1]
	}
	if len(parts) == 0 || len(parts) > 2 {
		return nil, fmt.Errorf("Unsupported language '%s' (expected: kr, en, cn, or a two-language combo like kr+en)", lang)
	}
	codes := make([]string, len(parts))
	for i, p := range parts {
		code, ok := languageCodes[p]
		if !ok {
			return nil, fmt.Errorf("Unsupported language '%s' (expected: kr, en, cn, or a two-language combo like kr+en)", lang)
		}
		codes[i] = code
	}
	return codes, nil
}

// mergeLanguages combines two language passes: primary segments keep their
// time windows; a secondary segment survives only if the primary pass left
// its region mostly uncovered (i.e. the primary language dropped that audio).
func mergeLanguages(primary, secondary []Segment) []Segment {
	out := append([]Segment{}, primary...)
	for _, seg := range secondary {
		dur := seg.End - seg.Start
		if dur < 1e-6 {
			dur = 1e-6
		}
		var covered float64
		for _, p := range primary {
			overlap := min(seg.End, p.End) - max(seg.Start, p.Start)
			if overlap > 0 {
				covered += overlap
			}
		}
		if covered/dur < LangMergeOverlap {
			out = append(out, seg)
		}
	}
	SortSegments(out)
	return out
}
