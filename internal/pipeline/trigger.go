package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Trigger launches a pipeline stage for an episode out of band from the request
// that produced the master. Two implementations back it: ExecTrigger spawns the
// local worker binary (dev/demo), CloudRunTrigger starts a Cloud Run Jobs
// execution (staging/prod). The provider hostnames and the admin/metadata REST
// shapes live only in this file and in server logs — the interface and its
// errors are neutral, so the api package (grepped by the vendor gate) can depend
// on it without leaking the stack.
type Trigger interface {
	Trigger(ctx context.Context, episodePublicID, stage string) error
}

// ExecTrigger runs the worker as a detached subprocess. It is for `make demo`
// and local dev, where the API server and worker share a host. The child is
// started and reaped asynchronously so upload-complete returns promptly.
type ExecTrigger struct {
	// Bin is the path to the compiled worker binary.
	Bin string
	// Log records the child's exit server-side.
	Log *slog.Logger
}

// NewExecTrigger returns an ExecTrigger for the worker binary at bin.
func NewExecTrigger(bin string, log *slog.Logger) *ExecTrigger {
	return &ExecTrigger{Bin: bin, Log: log}
}

// Trigger spawns `<bin> <episodePublicID> <stage>` detached from the caller.
// Starting is synchronous (so a misconfigured binary surfaces immediately);
// waiting is not, so the ffmpeg run does not block the HTTP response.
func (t *ExecTrigger) Trigger(ctx context.Context, episodePublicID, stage string) error {
	if strings.TrimSpace(t.Bin) == "" {
		return fmt.Errorf("pipeline: exec trigger has no worker binary configured")
	}
	// Detach from the request context: the worker must outlive this HTTP request.
	cmd := exec.Command(t.Bin, episodePublicID, stage) //nolint:gosec // Bin is operator config, args are ids.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("pipeline: start worker: %w", err)
	}
	log := t.Log
	if log == nil {
		log = slog.Default()
	}
	pid := cmd.Process.Pid
	go func() {
		werr := cmd.Wait()
		if werr != nil {
			log.LogAttrs(context.Background(), slog.LevelWarn, "worker subprocess exited nonzero",
				slog.Int("pid", pid), slog.String("episode", episodePublicID),
				slog.String("stage", stage), slog.String("error", werr.Error()))
			return
		}
		log.LogAttrs(context.Background(), slog.LevelInfo, "worker subprocess finished",
			slog.Int("pid", pid), slog.String("episode", episodePublicID), slog.String("stage", stage))
	}()
	return nil
}

// Provider endpoints. Confined to this file (and server logs): the Cloud Run
// Admin API and the instance metadata server that mints the access token.
const (
	defaultRunBaseURL = "https://run.googleapis.com"
	// metadataTokenURL returns an OAuth2 access token for the runtime service
	// account, scoped by the platform to what the account may do.
	metadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
)

// CloudRunTrigger starts a Cloud Run Jobs execution via the admin REST API,
// authenticating with an access token from the instance metadata server. It
// overrides the job's container args with the episode id and stage.
type CloudRunTrigger struct {
	Project string
	Region  string
	Job     string
	// HTTP is the client used for both the metadata and admin calls. Nil uses a
	// short-timeout default.
	HTTP *http.Client
	// BaseURL and TokenURL default to the real endpoints; tests point them at a
	// local fake server.
	BaseURL  string
	TokenURL string
	Log      *slog.Logger
}

// NewCloudRunTrigger builds a CloudRunTrigger for the given job coordinates.
func NewCloudRunTrigger(project, region, job string, log *slog.Logger) *CloudRunTrigger {
	return &CloudRunTrigger{
		Project: project, Region: region, Job: job,
		HTTP: &http.Client{Timeout: 15 * time.Second},
		Log:  log,
	}
}

type metadataToken struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// containerOverride is one entry in a Cloud Run Jobs run override.
type containerOverride struct {
	Args []string `json:"args"`
}

type runOverrides struct {
	ContainerOverrides []containerOverride `json:"containerOverrides"`
}

type runRequest struct {
	Overrides runOverrides `json:"overrides"`
}

// Trigger starts a job execution whose container runs `<episodePublicID>
// <stage>`. Any provider error is logged with its raw body server-side and
// returned to the caller as a neutral, id-tagged error — the api layer keeps it
// server-side regardless, but the mapping holds even if a future caller does
// not.
func (t *CloudRunTrigger) Trigger(ctx context.Context, episodePublicID, stage string) error {
	log := t.Log
	if log == nil {
		log = slog.Default()
	}
	client := t.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	token, err := t.fetchToken(ctx, client)
	if err != nil {
		id := newErrorID()
		log.LogAttrs(ctx, slog.LevelError, "worker trigger token fetch failed",
			slog.String("error_id", id), slog.String("error", err.Error()))
		return fmt.Errorf("pipeline: worker trigger unavailable (error_id=%s)", id)
	}

	base := t.BaseURL
	if base == "" {
		base = defaultRunBaseURL
	}
	url := fmt.Sprintf("%s/v2/projects/%s/locations/%s/jobs/%s:run",
		strings.TrimRight(base, "/"), t.Project, t.Region, t.Job)

	body, _ := json.Marshal(runRequest{Overrides: runOverrides{
		ContainerOverrides: []containerOverride{{Args: []string{episodePublicID, stage}}},
	}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("pipeline: build run request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		id := newErrorID()
		log.LogAttrs(ctx, slog.LevelError, "worker trigger request failed",
			slog.String("error_id", id), slog.String("error", err.Error()))
		return fmt.Errorf("pipeline: worker trigger unavailable (error_id=%s)", id)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		id := newErrorID()
		log.LogAttrs(ctx, slog.LevelError, "worker trigger rejected",
			slog.String("error_id", id), slog.Int("status", resp.StatusCode),
			slog.String("body", string(raw)))
		return fmt.Errorf("pipeline: worker trigger rejected (error_id=%s)", id)
	}
	log.LogAttrs(ctx, slog.LevelInfo, "worker execution started",
		slog.String("episode", episodePublicID), slog.String("stage", stage))
	return nil
}

// fetchToken retrieves an OAuth2 access token from the instance metadata server.
func (t *CloudRunTrigger) fetchToken(ctx context.Context, client *http.Client) (string, error) {
	tokenURL := t.TokenURL
	if tokenURL == "" {
		tokenURL = metadataTokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var tok metadataToken
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<10)).Decode(&tok); err != nil {
		return "", fmt.Errorf("decode token: %w", err)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return "", fmt.Errorf("empty access token")
	}
	return tok.AccessToken, nil
}
