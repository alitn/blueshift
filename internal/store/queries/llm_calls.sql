-- name: InsertLlmCall :one
-- Append-only audit of a single model call made through /internal/llm. There is
-- one row per provider call: successes, schema-invalid outputs, and failed or
-- retried attempts alike (the one-retry semantics live in /internal/llm; here
-- every attempt is recorded verbatim). org_id scopes the row to a tenant;
-- episode_id is nullable for calls not tied to one episode. raw_response holds
-- the provider response body verbatim (NULL when the upstream returned no valid
-- JSON body). cost_cents is NULL when no price is configured for the model;
-- latency_ms and status record the attempt's outcome.
INSERT INTO llm_calls (
    org_id, episode_id, model, prompt_version, input_hash,
    raw_response, cost_cents, latency_ms, status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
RETURNING *;
