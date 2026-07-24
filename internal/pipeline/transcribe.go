package pipeline

// transcribe.go is the second registered stage: it turns the ingest audio object
// into word-timed transcript segments and persists them, then (as the terminal
// stage today) finalizes the episode 'ready'. Everything provider-specific lives
// behind the internal/asr seam; this stage only orchestrates chunking, the
// engine call, the deterministic stitch, and the org-scoped persist.
//
// Verbatim invariant (CLAUDE.md): the engine's text and word timings are stored
// EXACTLY as returned — no normalization at rest. Timings come only from the ASR
// engine; the chunk offsets this stage adds are arithmetic on the measured audio
// length (asr.PlanChunks) and the deterministic asr.StitchTranscripts merge, not
// anything guessed here. The engine's raw metadata blob is logged server-side for
// the audit and never persisted to a client-visible surface.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"blueshift/internal/asr"
	"blueshift/internal/blob"
	"blueshift/internal/lang"
)

// ASR resolves the speech engine bound to a content language. The lang registry
// declares that the language uses an asr engine slot; the concrete engine behind
// a neutral label (config/wiring) is what this returns. Provider choice never
// crosses this seam — callers get an asr.Engine and a neutral label only.
type ASR interface {
	EngineFor(ctx context.Context, language string) (asr.Engine, error)
}

// SegmentStore persists an episode's transcript. ReplaceSegments is idempotent
// per episode (a re-run replaces rather than duplicates) and org-scoped, so a
// re-driven transcribe stage is safe and can never write across tenants.
// HasSegments is the cost-safety idempotency probe: the stage checks it BEFORE the
// billable ASR engine and skips the paid call when the transcript already exists.
type SegmentStore interface {
	ReplaceSegments(ctx context.Context, orgID, episodePublicID string, segments []asr.Segment) error
	// HasSegments reports whether the episode already has persisted transcript
	// segments (org-scoped). A true result means the transcribe stage must SKIP the
	// billable ASR call — never re-billing on a retry/re-drive (CLAUDE.md
	// "Billable-service cost safety").
	HasSegments(ctx context.Context, orgID, episodePublicID string) (bool, error)
}

// LangEngineResolver resolves the ASR engine for a content language from data,
// never a hardcoded provider: the lang registry declares that the language uses
// an asr engine slot (EngineKeys), and the neutral Label — bound at wiring time
// from config/env — selects a registered engine from Registry. An unregistered
// language, a language that declares no asr slot, or an unbound label is an
// explicit error, never a silent default. It implements ASR.
type LangEngineResolver struct {
	// Registry maps neutral engine labels to concrete engines (the fake for
	// demo/tests, the provider-backed engine in staging/prod).
	Registry *asr.Registry
	// Label is the neutral engine label bound to the asr slot (e.g. "bs-asr-1").
	// It never carries a provider name.
	Label string
}

var _ ASR = LangEngineResolver{}

// EngineFor resolves the engine for language. It first asks the lang registry for
// the language (which gates the tag and declares its engine slots — "engine
// choice resolved through /internal/lang"), verifies the language declares an asr
// slot, then resolves the wiring-bound neutral label to a registered engine.
func (r LangEngineResolver) EngineFor(_ context.Context, language string) (asr.Engine, error) {
	l, err := lang.Get(language)
	if err != nil {
		return nil, fmt.Errorf("transcribe: language %q not registered: %w", language, err)
	}
	if !declaresEngine(l, lang.EngineASR) {
		return nil, fmt.Errorf("transcribe: language %q declares no asr engine slot", language)
	}
	if r.Registry == nil {
		return nil, errors.New("transcribe: no asr engine registry configured")
	}
	engine, err := r.Registry.Get(r.Label)
	if err != nil {
		return nil, fmt.Errorf("transcribe: resolve engine %q: %w", r.Label, err)
	}
	return engine, nil
}

// declaresEngine reports whether l declares the given engine slot.
func declaresEngine(l lang.Language, key lang.EngineKey) bool {
	for _, k := range l.EngineKeys() {
		if k == key {
			return true
		}
	}
	return false
}

// runTranscribe adapts the transcribe stage to the registry's run signature. It
// reads the ingest audio object, transcribes it (chunking long audio), stitches,
// validates, and persists the segments — all under a per-attempt timeout so a
// wedged engine or ffmpeg is retried. It produces no proxy/duration outputs of
// its own: the terminal MarkReady preserves the ones ingest recorded (the
// stageOutput it returns forwards nothing, and MarkEpisodeReady COALESCEs).
//
// COST SAFETY (CLAUDE.md "Billable-service cost safety"). ASR is the billable
// engine, so two guards bound its cost before any paid call:
//   - Idempotency: if the episode already has segments, the paid call was already
//     made — SKIP it (no engine call, no attempt consumed). A plain retry/re-drive
//     never re-bills; only Config.Reprocess forces a fresh transcription.
//   - Attempt cap: BeginBillableAttempt increments process_attempts and refuses the
//     call once the per-episode ceiling (Config.maxProcessAttempts) is reached.
//
// Max billable calls per episode: internal/asr issues exactly ONE provider
// operation per engine.Transcribe (submit once, then poll a bounded loop — no retry
// re-submits, so no retry re-bills). One attempt calls Transcribe once per audio
// chunk (⌈duration / TranscribeChunkMs⌉). The per-run retry loop (1 + Config.Retries
// attempts) and every re-drive each increment process_attempts, and the stage
// refuses to call the engine once it reaches the cap — so an episode can ever
// trigger at most Config.maxProcessAttempts billable *attempts*
// (≤ maxProcessAttempts × chunks provider operations), shared with the diarize stage.
func (r *Runner) runTranscribe(parent context.Context, ep Episode, attempt int) (stageOutput, error) {
	ctx, cancel := context.WithTimeout(parent, r.Config.stageTimeout())
	defer cancel()

	if r.ASR == nil {
		return stageOutput{}, errors.New("transcribe: no ASR seam configured")
	}
	if r.Segments == nil {
		return stageOutput{}, errors.New("transcribe: no segment store configured")
	}

	// Idempotency guard: skip the billable ASR call when the transcript already
	// exists. This runs first (before engine resolution) so a re-drive of an
	// already-transcribed episode is a cheap, free no-op that just advances it.
	if !r.Config.Reprocess {
		has, err := r.Segments.HasSegments(ctx, ep.OrgID, ep.PublicID)
		if err != nil {
			return stageOutput{}, fmt.Errorf("transcribe: check existing segments: %w", err)
		}
		if has {
			r.logger().InfoContext(ctx, "already transcribed; skipping",
				slog.String("episode", ep.PublicID), slog.String("stage", string(StageTranscribe)))
			return stageOutput{}, nil
		}
	}

	engine, err := r.ASR.EngineFor(ctx, ep.Language)
	if err != nil {
		return stageOutput{}, err
	}

	audioKey, err := blob.ProxyKey(ep.OrgID, ep.PublicID, audioFilename)
	if err != nil {
		return stageOutput{}, fmt.Errorf("build audio key: %w", err)
	}

	totalMs := int(ep.DurationMs)
	if totalMs <= 0 {
		// The transcribe stage runs only after ingest measured the media length; a
		// missing duration means an out-of-order run, not audio we should guess at.
		return stageOutput{}, fmt.Errorf("transcribe: episode has no measured duration")
	}
	windows, err := asr.PlanChunks(totalMs, r.Config.transcribeChunkMs())
	if err != nil {
		return stageOutput{}, fmt.Errorf("plan chunks: %w", err)
	}

	// Glossary bias: the asr layer accepts recognition-bias hints (glossary terms
	// for the language) that only nudge spelling — the verbatim invariant holds
	// (see asr.TranscribeRequest.BiasTerms). No glossary_terms table exists yet, so
	// no terms are passed today; the bias source is wired when that table lands.
	var bias []string

	// Attempt cap: everything above is non-billable prep (engine resolve, chunk
	// planning) that can fail without consuming budget. Here — immediately before the
	// first paid engine.Transcribe — record the billable attempt and refuse it when
	// the per-episode ceiling is reached, so a capped episode bills NOTHING.
	billAttempt, allowed, err := r.Repo.BeginBillableAttempt(ctx, ep.OrgID, ep.PublicID, r.Config.maxProcessAttempts())
	if err != nil {
		return stageOutput{}, fmt.Errorf("transcribe: begin billable attempt: %w", err)
	}
	if !allowed {
		r.logger().ErrorContext(ctx, "transcribe blocked: per-episode billable attempt cap reached",
			slog.String("episode", ep.PublicID), slog.Int("max_process_attempts", r.Config.maxProcessAttempts()))
		return stageOutput{}, fmt.Errorf("%w (stage=transcribe max=%d)", ErrBillableCapReached, r.Config.maxProcessAttempts())
	}
	r.logger().InfoContext(ctx, "billable transcribe attempt",
		slog.String("episode", ep.PublicID), slog.Int("process_attempts", billAttempt),
		slog.Int("max_process_attempts", r.Config.maxProcessAttempts()))

	chunks, err := r.transcribeChunks(ctx, engine, ep, audioKey, windows, bias, attempt)
	if err != nil {
		return stageOutput{}, err
	}

	// Stitch shifts each chunk's ASR-relative timings by its source-time offset,
	// renumbers idx globally, and Validate-checks the merge. The explicit Validate
	// below is defence in depth (stitch already validates) and documents the
	// boundary invariant the persisted transcript must satisfy.
	stitched, err := asr.StitchTranscripts(engine.Label(), ep.Language, chunks)
	if err != nil {
		return stageOutput{}, fmt.Errorf("stitch transcripts: %w", err)
	}
	if err := stitched.Validate(); err != nil {
		return stageOutput{}, fmt.Errorf("validate transcript: %w", err)
	}

	if err := r.Segments.ReplaceSegments(ctx, ep.OrgID, ep.PublicID, stitched.Segments); err != nil {
		return stageOutput{}, fmt.Errorf("persist segments: %w", err)
	}
	r.logger().InfoContext(ctx, "transcribe complete",
		slog.String("episode", ep.PublicID), slog.String("engine", engine.Label()),
		slog.Int("chunks", len(chunks)), slog.Int("segments", len(stitched.Segments)))

	// No outputs to record: the terminal MarkReady preserves ingest's proxy key and
	// measured duration (MarkEpisodeReady COALESCEs a NULL arg).
	return stageOutput{}, nil
}

// transcribeChunks transcribes each planned window and returns the per-chunk
// results with their source-time offsets, ready for StitchTranscripts. A single
// window (audio at or under the chunk cap) transcribes the ingest audio object
// directly — no ffmpeg cut, no extra upload. Multiple windows are cut with ffmpeg
// into org-owned chunk objects the engine reads by key.
func (r *Runner) transcribeChunks(ctx context.Context, engine asr.Engine, ep Episode, audioKey string, windows [][2]int, bias []string, attempt int) ([]asr.ChunkResult, error) {
	if len(windows) == 1 {
		tr, err := r.transcribeOne(ctx, engine, audioKey, ep, bias)
		if err != nil {
			return nil, err
		}
		return []asr.ChunkResult{{StartMs: windows[0][0], Transcript: tr}}, nil
	}

	// Long audio: stage the audio locally once, then cut and transcribe each chunk.
	localAudio, releaseAudio, err := r.stageAudio(ctx, audioKey, attempt)
	if err != nil {
		return nil, err
	}
	defer releaseAudio()

	workDir, cleanup, err := tempDir(attempt)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	chunks := make([]asr.ChunkResult, 0, len(windows))
	for _, w := range windows {
		start, end := w[0], w[1]
		chunkKey, err := blob.ProxyKey(ep.OrgID, ep.PublicID, chunkFilename(start))
		if err != nil {
			return nil, fmt.Errorf("build chunk key: %w", err)
		}
		if err := r.stageChunk(ctx, localAudio, chunkKey, workDir, start, end-start); err != nil {
			return nil, err
		}
		tr, err := r.transcribeOne(ctx, engine, chunkKey, ep, bias)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, asr.ChunkResult{StartMs: start, Transcript: tr})
	}
	return chunks, nil
}

// transcribeOne transcribes a single audio object by key and audits the engine's
// raw metadata server-side. The engine reads the bytes from storage itself.
func (r *Runner) transcribeOne(ctx context.Context, engine asr.Engine, key string, ep Episode, bias []string) (asr.Transcript, error) {
	tr, err := engine.Transcribe(ctx, asr.TranscribeRequest{
		AudioKey:  key,
		Language:  ep.Language,
		BiasTerms: bias,
	})
	if err != nil {
		return asr.Transcript{}, fmt.Errorf("transcribe %q: %w", key, err)
	}
	// Engine raw metadata -> server-side audit log only. It may name a provider, so
	// it never reaches a client-visible surface; it stays in the server logs.
	if len(tr.Raw) > 0 {
		r.logger().InfoContext(ctx, "asr engine metadata",
			slog.String("episode", ep.PublicID), slog.String("engine", tr.Engine),
			slog.String("chunk_key", key), slog.String("raw", string(tr.Raw)))
	}
	return tr, nil
}

// stageAudio makes the ingest audio object available as a local path for
// ffmpeg-cutting. A filesystem-backed Blob (dev/demo direct mode) resolves the
// path in place; a remote Blob downloads it into a fresh work dir. The returned
// release frees any work dir (a no-op for direct mode).
func (r *Runner) stageAudio(ctx context.Context, audioKey string, attempt int) (localPath string, release func(), err error) {
	if lp, ok := r.Blob.(localPather); ok {
		p, perr := lp.LocalPath(audioKey)
		if perr != nil {
			return "", func() {}, fmt.Errorf("resolve audio path: %w", perr)
		}
		return p, func() {}, nil
	}
	dir, mkErr := os.MkdirTemp("", fmt.Sprintf("bs-transcribe-%d-*", attempt))
	if mkErr != nil {
		return "", func() {}, fmt.Errorf("transcribe temp dir: %w", mkErr)
	}
	dest := filepath.Join(dir, audioFilename)
	if err := r.Blob.Download(ctx, audioKey, dest); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("download audio: %w", err)
	}
	return dest, func() { _ = os.RemoveAll(dir) }, nil
}

// stageChunk cuts the [startMs, startMs+durationMs) window out of the local audio
// and makes it available at chunkKey for the engine. Direct mode cuts straight to
// the chunk's local path; remote mode cuts to the work dir and uploads it.
func (r *Runner) stageChunk(ctx context.Context, localAudio, chunkKey, workDir string, startMs, durationMs int) error {
	if lp, ok := r.Blob.(localPather); ok {
		dest, err := lp.LocalPath(chunkKey)
		if err != nil {
			return fmt.Errorf("resolve chunk path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return fmt.Errorf("mkdir chunk dir: %w", err)
		}
		if err := r.Media.CutAudio(ctx, localAudio, dest, startMs, durationMs); err != nil {
			return fmt.Errorf("cut chunk: %w", err)
		}
		return nil
	}
	tmp := filepath.Join(workDir, chunkFilename(startMs))
	if err := r.Media.CutAudio(ctx, localAudio, tmp, startMs, durationMs); err != nil {
		return fmt.Errorf("cut chunk: %w", err)
	}
	if err := r.Blob.Upload(ctx, chunkKey, tmp, audioContentType); err != nil {
		return fmt.Errorf("upload chunk: %w", err)
	}
	return nil
}

// chunkFilename is the fixed, code-supplied name for a chunk object under the
// episode's proxies/ prefix, keyed by its source-time start (zero-padded so the
// keys sort in time order). It is never client input.
func chunkFilename(startMs int) string {
	return fmt.Sprintf("audio-chunk-%09d.flac", startMs)
}
