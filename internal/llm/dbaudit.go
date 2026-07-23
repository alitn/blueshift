package llm

// dbaudit.go is the production Auditor: it persists each CallRecord to the
// llm_calls table through the store's generated query. Keeping this adapter here
// (rather than a database dependency inside the core) lets the rest of the
// package stay free of database types and lets tests audit into an in-memory
// sink. store is imported service->data, the normal direction.

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/store"
	"blueshift/internal/store/db"
)

// DBAuditor writes audit rows to Postgres via the store.
type DBAuditor struct {
	st *store.Store
}

var _ Auditor = (*DBAuditor)(nil)

// NewDBAuditor wraps a store as the production Auditor.
func NewDBAuditor(st *store.Store) *DBAuditor { return &DBAuditor{st: st} }

// RecordLLMCall inserts one llm_calls row. A nil RawResponse or CostCents, or a
// zero EpisodeID, is written as SQL NULL.
func (a *DBAuditor) RecordLLMCall(ctx context.Context, rec CallRecord) error {
	_, err := a.st.InsertLlmCall(ctx, db.InsertLlmCallParams{
		OrgID:         rec.OrgID,
		EpisodeID:     optInt8(rec.EpisodeID),
		Model:         rec.Model,
		PromptVersion: rec.PromptVersion,
		InputHash:     rec.InputHash,
		RawResponse:   rec.RawResponse,
		CostCents:     optInt4(rec.CostCents),
		LatencyMs:     pgtype.Int4{Int32: clampInt32(rec.LatencyMS), Valid: true},
		Status:        optText(rec.Status),
	})
	if err != nil {
		return fmt.Errorf("llm: record call: %w", err)
	}
	return nil
}

// optInt8 maps a zero id to SQL NULL (episode_id is optional).
func optInt8(v int64) pgtype.Int8 {
	if v == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: v, Valid: true}
}

// optInt4 maps a nil *int to SQL NULL (unknown cost).
func optInt4(p *int) pgtype.Int4 {
	if p == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: clampInt32(*p), Valid: true}
}

// optText maps an empty string to SQL NULL.
func optText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// clampInt32 narrows an int to int32, saturating rather than wrapping so an
// absurd latency can never corrupt the row.
func clampInt32(v int) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	default:
		return int32(v)
	}
}
