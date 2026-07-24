package server

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// middleware wraps an http.Handler.
type middleware func(http.Handler) http.Handler

// chain applies middlewares so the first listed runs outermost.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// statusRecorder captures the response status code for logging without
// buffering the body.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.written {
		r.status = code
		r.written = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.written {
		r.status = http.StatusOK
		r.written = true
	}
	return r.ResponseWriter.Write(b)
}

// requestLogger logs one structured line per request: method, path, status,
// duration. It never logs bodies, headers, or query strings.
func requestLogger(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			)
		})
	}
}

// noStore stamps Cache-Control: no-store before the handler runs, so API and
// health responses are never cached by browsers or intermediaries. Applied once
// at mount time (the /api subtree and the health endpoints) — a defensive
// default that covers every current and future handler without per-handler
// edits. Handlers remain free to overwrite it if one ever needs a real policy.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// recoverer turns a handler panic into a 500 and an ERROR log line instead of
// crashing the process. It sits inside the request logger so the recovered
// request is still logged with its 500 status.
func recoverer(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					// http.ErrAbortHandler is the sanctioned way to abort a
					// response; propagate it rather than swallowing.
					if p == http.ErrAbortHandler {
						panic(p)
					}
					logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
						slog.Any("panic", p),
						slog.String("stack", string(debug.Stack())),
					)
					writeJSON(w, http.StatusInternalServerError, map[string]string{
						"error": "internal server error",
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
