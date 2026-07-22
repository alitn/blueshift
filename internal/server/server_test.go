package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"blueshift/internal/config"
)

func testServer(t *testing.T, h http.Handler) (*http.Server, net.Listener) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	return srv, ln
}

func TestServeAndGracefulShutdown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)

	srv, ln := testServer(t, mux)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, ln, discardLogger()) }()

	url := "http://" + ln.Addr().String() + "/healthz"
	if err := waitForServer(url); err != nil {
		t.Fatalf("server did not come up: %v", err)
	}

	resp, err := http.Get(url) //nolint:noctx // simple local smoke request
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned error on clean shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancel")
	}
}

func TestServeDrainsInflightRequest(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})

	srv, ln := testServer(t, mux)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, ln, discardLogger()) }()

	url := "http://" + ln.Addr().String() + "/slow"
	if err := waitForServer("http://" + ln.Addr().String() + "/missing"); err != nil {
		t.Fatalf("server did not come up: %v", err)
	}

	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get(url) //nolint:noctx // simple local smoke request
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	<-started // request is in flight inside the handler
	cancel()  // signal shutdown while the request is unfinished
	close(release)

	select {
	case resp := <-respCh:
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "done" {
			t.Fatalf("in-flight request not drained cleanly: status=%d body=%q", resp.StatusCode, body)
		}
	case err := <-errCh:
		t.Fatalf("in-flight request failed during shutdown: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

func TestNewRoutesHealthAndReady(t *testing.T) {
	cfg := config.Config{Port: "0"}
	ready := NewReadiness()
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ui"))
	})
	apiStub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api"))
	})
	srv := New(cfg, discardLogger(), ui, ready, apiStub)

	cases := []struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		{"/healthz", http.StatusOK, `{"status":"ok"}` + "\n"},
		{"/readyz", http.StatusOK, `{"status":"ready","checks":{}}` + "\n"},
		{"/api/anything", http.StatusOK, "api"},
		{"/", http.StatusOK, "ui"},
		{"/anything", http.StatusOK, "ui"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		if rec.Code != c.wantStatus {
			t.Errorf("%s status = %d, want %d", c.path, rec.Code, c.wantStatus)
		}
		if c.wantBody != "" && rec.Body.String() != c.wantBody {
			t.Errorf("%s body = %q, want %q", c.path, rec.Body.String(), c.wantBody)
		}
	}
}

func waitForServer(url string) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // startup probe
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return context.DeadlineExceeded
}
