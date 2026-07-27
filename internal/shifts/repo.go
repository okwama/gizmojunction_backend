// Package shifts records real cashier shift sessions — opened at actual
// login, closed at actual logout or at cash-up submission (whichever
// happens first). Deliberately NOT derived from refresh_tokens: the access
// token silently rotates every ~15 minutes while someone's active
// (auth.Service.issueTokens is called by Login, Refresh, AND Signup), so a
// single refresh_tokens row's lifetime is one slice of a session, not the
// whole shift. Login/Logout in internal/auth are the only two places a
// real login/logout event happens.
package shifts

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// OpenIfNoneOpen starts a new shift unless one is already open for this
// cashier — the partial unique index on shifts(cashier_id) WHERE ended_at
// IS NULL makes a second concurrent open a harmless no-op (multiple tabs,
// a double-click login).
func (r *Repo) OpenIfNoneOpen(ctx context.Context, cashierID string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO shifts (cashier_id) VALUES ($1)
		ON CONFLICT (cashier_id) WHERE ended_at IS NULL DO NOTHING`, cashierID)
	return err
}

// CloseOpenForCashier ends whichever shift is currently open for this
// cashier, if any. reason is "logout" or "cash_up".
func (r *Repo) CloseOpenForCashier(ctx context.Context, cashierID, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE shifts SET ended_at = now(), ended_reason = $2
		WHERE cashier_id = $1 AND ended_at IS NULL`, cashierID, reason)
	return err
}

// CurrentShiftStart returns the open shift's started_at, or nil if this
// cashier has no open shift (an ADMIN account, or a CASHIER account
// predating this feature).
func (r *Repo) CurrentShiftStart(ctx context.Context, cashierID string) (*time.Time, error) {
	var startedAt time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT started_at FROM shifts WHERE cashier_id = $1 AND ended_at IS NULL`, cashierID).Scan(&startedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &startedAt, nil
}

type ActiveShift struct {
	CashierID   string    `db:"cashier_id" json:"cashier_id"`
	CashierName *string   `db:"cashier_name" json:"cashier_name,omitempty"`
	StartedAt   time.Time `db:"started_at" json:"started_at"`
}

// ListActive backs an admin "who's on the till right now" view.
func (r *Repo) ListActive(ctx context.Context) ([]ActiveShift, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.cashier_id::text, p.full_name AS cashier_name, s.started_at
		FROM shifts s
		LEFT JOIN profiles p ON p.id = s.cashier_id
		WHERE s.ended_at IS NULL
		ORDER BY s.started_at`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[ActiveShift])
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []ActiveShift{}
	}
	return list, nil
}
