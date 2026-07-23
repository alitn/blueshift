package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"blueshift/internal/blob"
	"blueshift/internal/media"
)

// Output filenames under the episode's proxies/ prefix. Fixed, code-supplied
// (never client input): the browser proxy and the mono 16 kHz ASR audio.
const (
	proxyFilename = "proxy-720p.mp4"
	audioFilename = "audio.flac"

	proxyContentType = "video/mp4"
	audioContentType = "audio/flac"
)

// attemptIngest runs one ingest attempt in a fresh work dir: stage the master,
// measure its duration, render the proxy and extract the audio, then persist the
// outputs. It returns the proxy object key and the measured duration in ms.
//
// On a filesystem-backed Blob it operates on objects in place (no copy); on a
// remote Blob it downloads the master into the work dir and uploads the renders.
// A per-attempt timeout is layered on the parent context so a wedged ffmpeg is
// killed and the attempt can be retried.
func (r *Runner) attemptIngest(parent context.Context, ep Episode, attempt int) (proxyKey string, durationMs int64, err error) {
	ctx, cancel := context.WithTimeout(parent, r.Config.stageTimeout())
	defer cancel()

	proxyKey, err = blob.ProxyKey(ep.OrgID, ep.PublicID, proxyFilename)
	if err != nil {
		return "", 0, fmt.Errorf("build proxy key: %w", err)
	}
	audioKey, err := blob.ProxyKey(ep.OrgID, ep.PublicID, audioFilename)
	if err != nil {
		return "", 0, fmt.Errorf("build audio key: %w", err)
	}

	workDir, cleanup, err := tempDir(attempt)
	if err != nil {
		return "", 0, err
	}
	defer cleanup()

	var masterPath, proxyPath, audioPath string
	direct := false
	if lp, ok := r.Blob.(localPather); ok {
		// Direct-path mode: read the master and write the renders in place.
		if masterPath, err = lp.LocalPath(ep.MasterObjectKey); err != nil {
			return "", 0, fmt.Errorf("resolve master path: %w", err)
		}
		if proxyPath, err = lp.LocalPath(proxyKey); err != nil {
			return "", 0, fmt.Errorf("resolve proxy path: %w", err)
		}
		if audioPath, err = lp.LocalPath(audioKey); err != nil {
			return "", 0, fmt.Errorf("resolve audio path: %w", err)
		}
		if err = os.MkdirAll(filepath.Dir(proxyPath), 0o750); err != nil {
			return "", 0, fmt.Errorf("mkdir proxies: %w", err)
		}
		direct = true
	} else {
		// Remote mode: download the master, render to the work dir, upload back.
		masterPath = filepath.Join(workDir, "master")
		proxyPath = filepath.Join(workDir, proxyFilename)
		audioPath = filepath.Join(workDir, audioFilename)
		if err = r.Blob.Download(ctx, ep.MasterObjectKey, masterPath); err != nil {
			return "", 0, fmt.Errorf("download master: %w", err)
		}
	}

	// Probe the master once: the duration is measured from the container
	// (verbatim invariant, never derived) and the same summary drives the
	// remux/transcode ruling below.
	probe, err := r.Media.Probe(ctx, masterPath)
	if err != nil {
		return "", 0, fmt.Errorf("probe master: %w", err)
	}
	durationMs = probe.Duration.Milliseconds()

	// Rule: an already-browser-compatible master is remuxed (stream copy, seconds);
	// otherwise it is transcoded (minutes). The probe summary and ruling are logged
	// server-side only — they name codecs/dimensions, never a provider.
	decision := media.EligibleForRemux(probe, r.Config.maxRemuxBitrate())
	r.logger().InfoContext(ctx, "master probed",
		slog.String("episode", ep.PublicID), slog.Int("attempt", attempt),
		slog.Any("probe", probe), slog.Bool("remux", decision.Remux),
		slog.String("ruling", decision.Reason))

	if decision.Remux {
		if err = r.Media.RemuxProxy(ctx, masterPath, proxyPath); err != nil {
			return "", 0, fmt.Errorf("remux proxy: %w", err)
		}
	} else {
		if err = r.Media.RenderProxy(ctx, masterPath, proxyPath); err != nil {
			return "", 0, fmt.Errorf("render proxy: %w", err)
		}
	}
	// Audio extraction is unchanged and runs in both paths (ASR needs it).
	if err = r.Media.ExtractAudio(ctx, masterPath, audioPath); err != nil {
		return "", 0, fmt.Errorf("extract audio: %w", err)
	}

	if !direct {
		if err = r.Blob.Upload(ctx, proxyKey, proxyPath, proxyContentType); err != nil {
			return "", 0, fmt.Errorf("upload proxy: %w", err)
		}
		if err = r.Blob.Upload(ctx, audioKey, audioPath, audioContentType); err != nil {
			return "", 0, fmt.Errorf("upload audio: %w", err)
		}
	}

	return proxyKey, durationMs, nil
}
