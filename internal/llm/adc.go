package llm

// adc.go bridges the module's existing oauth2 plumbing to the gemini engine's
// bearer-token seam. It is the production credential path for the platform's
// generateContent endpoint; like the GCS client, its network behaviour is
// exercised in staging, not in unit tests (tests inject a static tokenFn), so no
// credential is ever needed offline.

import (
	"context"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// adcTokenFunc returns a tokenFn backed by Application Default Credentials. The
// underlying token source is created lazily on first use and cached; oauth2
// handles refresh, so each call returns a currently-valid access token.
func adcTokenFunc(scopes ...string) tokenFn {
	var (
		once    sync.Once
		ts      oauth2.TokenSource
		initErr error
	)
	return func(ctx context.Context) (string, error) {
		once.Do(func() {
			ts, initErr = google.DefaultTokenSource(ctx, scopes...)
		})
		if initErr != nil {
			return "", initErr
		}
		tok, err := ts.Token()
		if err != nil {
			return "", err
		}
		return tok.AccessToken, nil
	}
}
