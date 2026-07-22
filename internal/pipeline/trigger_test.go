package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- exec trigger ------------------------------------------------------------

func TestExecTriggerSpawnsBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip: POSIX shell stub not available on windows")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	stub := filepath.Join(dir, "stub.sh")
	// The stub records its two args so the test can assert the worker was invoked
	// as `<bin> <episode_public_id> <stage>`.
	script := "#!/bin/sh\nprintf '%s %s' \"$1\" \"$2\" > " + shellQuote(marker) + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil { //nolint:gosec // test stub must be executable.
		t.Fatalf("write stub: %v", err)
	}

	tr := NewExecTrigger(stub, discard())
	if err := tr.Trigger(context.Background(), "ep_xyz", "ingest"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	// The child runs asynchronously; poll briefly for its marker.
	deadline := time.Now().Add(3 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(marker); err == nil {
			got = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if string(got) != "ep_xyz ingest" {
		t.Errorf("stub recorded %q, want %q", string(got), "ep_xyz ingest")
	}
}

func TestExecTriggerNoBinary(t *testing.T) {
	tr := NewExecTrigger("  ", discard())
	if err := tr.Trigger(context.Background(), "ep_x", "ingest"); err == nil {
		t.Fatal("Trigger with no binary: want error, got nil")
	}
}

// --- cloudrun trigger --------------------------------------------------------

func TestCloudRunTriggerStartsExecution(t *testing.T) {
	var gotPath, gotAuth, gotMetaFlavor string
	var gotArgs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			gotMetaFlavor = r.Header.Get("Metadata-Flavor")
			_ = json.NewEncoder(w).Encode(metadataToken{AccessToken: "test-token", TokenType: "Bearer", ExpiresIn: 3599})
		case strings.Contains(r.URL.Path, ":run"):
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			var body runRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Overrides.ContainerOverrides) == 1 {
				gotArgs = body.Overrides.ContainerOverrides[0].Args
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "executions/exec-1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tr := &CloudRunTrigger{
		Project: "p", Region: "r", Job: "j",
		HTTP:     srv.Client(),
		BaseURL:  srv.URL,
		TokenURL: srv.URL + "/token",
		Log:      discard(),
	}
	if err := tr.Trigger(context.Background(), "ep_9", "ingest"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	if want := "/v2/projects/p/locations/r/jobs/j:run"; gotPath != want {
		t.Errorf("run path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", gotAuth)
	}
	if gotMetaFlavor != "Google" {
		t.Errorf("token request Metadata-Flavor = %q, want Google", gotMetaFlavor)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "ep_9" || gotArgs[1] != "ingest" {
		t.Errorf("container args = %v, want [ep_9 ingest]", gotArgs)
	}
}

func TestCloudRunTriggerNeutralOnReject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			_ = json.NewEncoder(w).Encode(metadataToken{AccessToken: "test-token"})
			return
		}
		// Provider-flavoured error body — must never surface to the caller.
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"google backend quota exceeded"}}`))
	}))
	defer srv.Close()

	tr := &CloudRunTrigger{
		Project: "p", Region: "r", Job: "j",
		HTTP: srv.Client(), BaseURL: srv.URL, TokenURL: srv.URL + "/token", Log: discard(),
	}
	err := tr.Trigger(context.Background(), "ep_9", "ingest")
	if err == nil {
		t.Fatal("Trigger on 500: want error, got nil")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "error_id=") {
		t.Errorf("error %q missing neutral error_id", err.Error())
	}
	for _, leak := range []string{"google", "quota", "500", "backend"} {
		if strings.Contains(msg, leak) {
			t.Errorf("error %q leaked provider detail %q", err.Error(), leak)
		}
	}
}

func TestCloudRunTriggerNeutralOnTokenFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden) // token endpoint denies
	}))
	defer srv.Close()

	tr := &CloudRunTrigger{
		Project: "p", Region: "r", Job: "j",
		HTTP: srv.Client(), BaseURL: srv.URL, TokenURL: srv.URL + "/token", Log: discard(),
	}
	err := tr.Trigger(context.Background(), "ep_9", "ingest")
	if err == nil {
		t.Fatal("Trigger with token failure: want error, got nil")
	}
	if !strings.Contains(err.Error(), "error_id=") {
		t.Errorf("error %q missing neutral error_id", err.Error())
	}
}

// shellQuote single-quotes s for safe embedding in the POSIX stub script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
