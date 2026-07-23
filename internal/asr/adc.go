package asr

// adc.go bridges the module's existing oauth2 plumbing to the speech engine's
// bearer-token seam, mirroring /internal/llm/adc.go. It is the production
// credential path for the platform's speech endpoint; like the GCS client, its
// network behaviour is exercised in staging, not in unit tests (tests inject a
// static tokenFunc), so no credential is ever needed offline.

import (
	"context"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// speechCloudScope is the OAuth scope an ADC token must carry to call the
// platform's speech endpoint. It is the same broad cloud-platform scope the
// llm engines use; the calling service account's IAM roles (not the token
// scope) constrain what it may actually do.
const speechCloudScope = "https://www.googleapis.com/auth/cloud-platform"

// adcTokenFunc returns a tokenFunc backed by Application Default Credentials.
// The underlying token source is created lazily on first use and cached; oauth2
// handles refresh, so each call returns a currently-valid access token.
func adcTokenFunc(scopes ...string) tokenFunc {
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
