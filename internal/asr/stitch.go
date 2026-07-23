package asr

// stitch.go holds the deterministic long-audio merge: the pure functions an
// upstream stage uses to transcribe audio longer than a single batch call can
// safely cover. The documented batch constraint is 1 minute to 1 hour in general,
// but only up to ~20 minutes when word-level timestamps are enabled (the generic
// Speech batch limit). Rather than risk a silent truncation on the
// 40-minute-plus interviews this product ingests, the safe path is: split the
// audio into <=15-min chunks (a margin under the 20-min cap), transcribe each
// chunk's key with the engine, and stitch the per-chunk transcripts back into one
// whole-audio transcript here.
//
// These functions are pure (no I/O, no provider), so the ms-offset arithmetic is
// exhaustively unit-tested offline. They name no provider.

import (
	"fmt"
	"sort"
)

// ChunkResult pairs one chunk's transcript — whose segment/word timings are
// RELATIVE to the start of that chunk — with the chunk's start offset in the full
// audio, in integer milliseconds. A driver that cut the audio at t=0,
// 900_000ms, 1_800_000ms passes StartMs 0, 900_000, 1_800_000 for the three
// chunks it transcribed.
type ChunkResult struct {
	// StartMs is where this chunk begins in the full audio (ms). Chunks must be
	// contiguous and non-overlapping in source time; StartMs must strictly
	// increase across the slice (enforced by StitchTranscripts).
	StartMs int
	// Transcript is the engine's output for this chunk, with chunk-relative timing.
	Transcript Transcript
}

// StitchTranscripts merges chunk transcripts into one whole-audio Transcript for
// engine + language. Each chunk's segment and word timings are shifted by that
// chunk's StartMs, segment Idx is renumbered globally in time order, and the
// result is Validate-checked so a bad merge (or an inconsistent StartMs) fails
// here rather than downstream.
//
// Boundary handling: the merge assumes the driver cut the audio at exact,
// non-overlapping boundaries (e.g. ffmpeg -segment at silence-adjacent points)
// and passed each chunk's true source-time StartMs. It shifts and concatenates;
// it does NOT de-duplicate. If a driver uses OVERLAPPING chunks (to avoid cutting
// a word), it must trim the overlap before calling — otherwise the shifted
// segments overlap and Validate rejects the result. StartMs values must strictly
// increase; equal or decreasing offsets are a caller error.
func StitchTranscripts(engine, language string, chunks []ChunkResult) (Transcript, error) {
	ordered := make([]ChunkResult, len(chunks))
	copy(ordered, chunks)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].StartMs < ordered[j].StartMs })

	out := Transcript{Engine: engine, Language: language}
	prevStart := -1
	idx := 0
	for ci, ch := range ordered {
		if ch.StartMs < 0 {
			return Transcript{}, fmt.Errorf("%w: chunk %d has negative start offset %d", ErrInvalidTranscript, ci, ch.StartMs)
		}
		if ch.StartMs <= prevStart {
			return Transcript{}, fmt.Errorf("%w: chunk %d start offset %d does not exceed previous %d", ErrInvalidTranscript, ci, ch.StartMs, prevStart)
		}
		prevStart = ch.StartMs
		for _, seg := range ch.Transcript.Segments {
			shifted := Segment{
				Idx:     idx,
				StartMs: seg.StartMs + ch.StartMs,
				EndMs:   seg.EndMs + ch.StartMs,
				Text:    seg.Text,
			}
			if seg.Words != nil {
				shifted.Words = make([]Word, len(seg.Words))
				for wi, w := range seg.Words {
					shifted.Words[wi] = Word{
						Text:    w.Text,
						StartMs: w.StartMs + ch.StartMs,
						EndMs:   w.EndMs + ch.StartMs,
						Conf:    w.Conf,
					}
				}
			}
			out.Segments = append(out.Segments, shifted)
			idx++
		}
	}
	if err := out.Validate(); err != nil {
		return Transcript{}, err
	}
	return out, nil
}

// PlanChunks splits a total audio duration into contiguous, non-overlapping
// windows of at most maxChunkMs each, returning [startMs, endMs) pairs. It is the
// deterministic boundary planner a driver feeds to the segmenter (the last window
// is whatever remains). Both arguments must be positive.
func PlanChunks(totalMs, maxChunkMs int) ([][2]int, error) {
	if totalMs <= 0 {
		return nil, fmt.Errorf("asr: total duration must be positive, got %d", totalMs)
	}
	if maxChunkMs <= 0 {
		return nil, fmt.Errorf("asr: max chunk duration must be positive, got %d", maxChunkMs)
	}
	var windows [][2]int
	for start := 0; start < totalMs; start += maxChunkMs {
		end := start + maxChunkMs
		if end > totalMs {
			end = totalMs
		}
		windows = append(windows, [2]int{start, end})
	}
	return windows, nil
}
