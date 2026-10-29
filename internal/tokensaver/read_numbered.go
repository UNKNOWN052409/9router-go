package tokensaver

import (
	"fmt"
	"regexp"
	"strings"
)

// The read-numbered shape: a Cursor/Codex read_file result, where every line
// is prefixed with its line number ("  1|content").
//
// Upstream has no tool-name protection at all — it selects its filter by the
// shape of the content (open-sse/rtk/autodetect.js). This port keeps a
// tool-name protected list, so a read or a grep result reaches the model
// intact by default. The one shape upstream truncates safely is a long
// line-numbered file dump: the head and the tail are kept and the cut is
// marked, so the content stays recognisable while the middle is dropped.
//
// The thresholds are upstream's, unchanged
// (open-sse/rtk/constants.js, open-sse/rtk/filters/readNumbered.js):
//
//	SMART_TRUNCATE_MIN_LINES  = 250  — a shorter dump is not truncated
//	SMART_TRUNCATE_HEAD       = 120  — lines kept from the top
//	SMART_TRUNCATE_TAIL       =  60  — lines kept from the bottom
//	READ_NUMBERED_MIN_HIT_RATIO = 0.7 — share of sampled lines that must look
//	                                  like "  N|content"
//
// Upstream has ONE threshold constant and TWO filters that compare against
// it — open-sse/rtk/filters/smartTruncate.js and
// open-sse/rtk/filters/readNumbered.js — both with a `<` comparison: a dump
// of exactly 250 lines IS truncated, a shorter one is not. This package
// mirrors that with one definition too: SmartTruncMin. The exported
// ReadNumberedMinLines below is an alias of it, not a second copy of the
// number.
const (
	// ReadNumberedMinLines is upstream's SMART_TRUNCATE_MIN_LINES. A dump
	// with fewer lines than this is left alone.
	//
	// It aliases SmartTruncMin so the number exists once in the package:
	// this path and smartTruncate read the same constant, exactly as
	// upstream's two filters read the same one.
	ReadNumberedMinLines = SmartTruncMin

	// ReadNumberedMinHitRatio is upstream's READ_NUMBERED_MIN_HIT_RATIO.
	ReadNumberedMinHitRatio = 0.7

	// ReadNumberedSampleLines is the number of leading lines the hit ratio
	// is computed over, and ReadNumberedMinNonEmptyLines the smallest
	// sample that can carry a meaningful ratio. Both mirror upstream's
	// isLineNumbered.
	ReadNumberedSampleLines      = 100
	ReadNumberedMinNonEmptyLines = 5
)

// reReadNumberedLine matches one "  N|content" line. It is upstream's
// READ_NUMBERED_LINE_RE.
var reReadNumberedLine = regexp.MustCompile(`^\s*\d+\|`)

// isLineNumberedDump reports whether s has the shape of a line-numbered file
// dump: at least ReadNumberedMinLines lines, and at least
// ReadNumberedMinHitRatio of the sampled leading lines look like "  N|content".
func isLineNumberedDump(s string) bool {
	lines := strings.Split(s, "\n")
	if len(lines) < ReadNumberedMinLines {
		return false
	}
	return isLineNumberedSample(lines)
}

// isLineNumberedSample applies upstream's shape test to the sampled lines.
//
// The sample is the first ReadNumberedSampleLines lines. Empty lines are
// skipped; every other line counts towards the denominator, and a line counts
// as a hit when it looks like "  N|content". A sample with fewer than
// ReadNumberedMinNonEmptyLines non-empty lines cannot carry a meaningful
// ratio, so it is not the shape.
func isLineNumberedSample(lines []string) bool {
	sample := lines
	if len(sample) > ReadNumberedSampleLines {
		sample = sample[:ReadNumberedSampleLines]
	}
	nonEmpty, hits := 0, 0
	for _, line := range sample {
		if line == "" {
			continue
		}
		nonEmpty++
		if reReadNumberedLine.MatchString(line) {
			hits++
		}
	}
	if nonEmpty < ReadNumberedMinNonEmptyLines {
		return false
	}
	return float64(hits)/float64(nonEmpty) >= ReadNumberedMinHitRatio
}

// compressReadNumbered truncates a line-numbered dump the way upstream's
// read-numbered filter does: the first SmartTruncHead lines and the last
// SmartTruncTail lines are kept, and the cut is marked with a visible line.
//
// It is a truncation, not a rewrite: every kept line is byte-for-byte what
// the tool returned, and the marker says how much was left out.
func compressReadNumbered(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) < ReadNumberedMinLines {
		return s
	}
	head := lines[:SmartTruncHead]
	tail := lines[len(lines)-SmartTruncTail:]
	cut := len(lines) - len(head) - len(tail)

	result := make([]string, 0, len(head)+len(tail)+1)
	result = append(result, head...)
	result = append(result, fmt.Sprintf("... +%d lines truncated (file continues)", cut))
	result = append(result, tail...)
	return strings.Join(result, "\n")
}

