package auth

import (
	"context"
	"errors"
	"testing"
)

type fakeQuerier struct {
	row   AuthRow
	found bool
	err   error
}

func (f fakeQuerier) AuthContextByEmail(context.Context, string) (AuthRow, bool, error) {
	return f.row, f.found, f.err
}

type fakeDir struct {
	ac  AuthContext
	err error
}

func (f fakeDir) LookupByEmail(context.Context, string) (AuthContext, error) {
	return f.ac, f.err
}

var sampleRow = AuthRow{
	Email:       "dev-approver@blueshift.local",
	DisplayName: "Dev Approver",
	OrgPublicID: "0192f0aa-1111-7abc-8def-000000000001",
	OrgName:     "Blueshift Pilot",
	Role:        "approver",
}

func TestStoreDirectory(t *testing.T) {
	ctx := context.Background()

	t.Run("nil querier reports unavailable", func(t *testing.T) {
		d := NewStoreDirectory(nil)
		if _, err := d.LookupByEmail(ctx, "x"); !errors.Is(err, ErrAuthUnavailable) {
			t.Fatalf("err = %v, want ErrAuthUnavailable", err)
		}
	})

	t.Run("query error is unavailable", func(t *testing.T) {
		d := NewStoreDirectory(fakeQuerier{err: errors.New("connection refused")})
		if _, err := d.LookupByEmail(ctx, "x"); !errors.Is(err, ErrAuthUnavailable) {
			t.Fatalf("err = %v, want ErrAuthUnavailable", err)
		}
	})

	t.Run("no user is unknown", func(t *testing.T) {
		d := NewStoreDirectory(fakeQuerier{found: false})
		if _, err := d.LookupByEmail(ctx, "x"); !errors.Is(err, ErrUnknownUser) {
			t.Fatalf("err = %v, want ErrUnknownUser", err)
		}
	})

	t.Run("found returns context", func(t *testing.T) {
		d := NewStoreDirectory(fakeQuerier{row: sampleRow, found: true})
		ac, err := d.LookupByEmail(ctx, "dev-approver@blueshift.local")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if ac.Email != sampleRow.Email || ac.OrgName != sampleRow.OrgName || ac.Role != sampleRow.Role {
			t.Errorf("ac = %+v, want mapped from %+v", ac, sampleRow)
		}
	})
}

func TestDevAuthenticator(t *testing.T) {
	ctx := context.Background()
	ac := AuthContext(sampleRow)

	t.Run("correct password", func(t *testing.T) {
		a := DevAuthenticator{Password: "pw", Dir: fakeDir{ac: ac}}
		got, err := a.Authenticate(ctx, "dev-approver@blueshift.local", "pw")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Role != "approver" {
			t.Errorf("role = %q, want approver", got.Role)
		}
	})

	t.Run("wrong password fails", func(t *testing.T) {
		a := DevAuthenticator{Password: "pw", Dir: fakeDir{ac: ac}}
		if _, err := a.Authenticate(ctx, "dev-approver@blueshift.local", "nope"); !errors.Is(err, ErrAuthFailed) {
			t.Fatalf("err = %v, want ErrAuthFailed", err)
		}
	})

	t.Run("unknown user propagates", func(t *testing.T) {
		a := DevAuthenticator{Password: "pw", Dir: fakeDir{err: ErrUnknownUser}}
		if _, err := a.Authenticate(ctx, "ghost@x", "pw"); !errors.Is(err, ErrUnknownUser) {
			t.Fatalf("err = %v, want ErrUnknownUser", err)
		}
	})

	t.Run("backend unavailable propagates", func(t *testing.T) {
		a := DevAuthenticator{Password: "pw", Dir: fakeDir{err: ErrAuthUnavailable}}
		if _, err := a.Authenticate(ctx, "dev-approver@blueshift.local", "pw"); !errors.Is(err, ErrAuthUnavailable) {
			t.Fatalf("err = %v, want ErrAuthUnavailable", err)
		}
	})
}
