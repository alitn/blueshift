package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"blueshift/internal/auth"
)

// AuthContextByEmail implements auth.AuthQuerier: it resolves a seeded user's
// org + role by email. A missing user is (found=false, err=nil); any other
// database error is returned so the caller maps it to ErrAuthUnavailable.
func (s *Store) AuthContextByEmail(ctx context.Context, email string) (auth.AuthRow, bool, error) {
	row, err := s.GetAuthContextByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.AuthRow{}, false, nil
	}
	if err != nil {
		return auth.AuthRow{}, false, fmt.Errorf("store: auth context by email: %w", err)
	}
	return auth.AuthRow{
		Email:       row.UserEmail,
		DisplayName: row.UserDisplayName,
		OrgPublicID: uuidString(row.OrgPublicID),
		OrgName:     row.OrgName,
		Role:        row.Role,
	}, true, nil
}

// uuidString renders a pgtype.UUID as canonical 8-4-4-4-12 hex. An invalid
// (NULL) uuid renders empty.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
