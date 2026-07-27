// Package cashups covers end-of-day cash-up / Z-report: a cashier's
// expected cash (from their own POS sales since their last cash-up) versus
// what's physically counted, with a per-payment-method breakdown.
//
// "Shift" isn't a first-class entity here — period_start is simply the
// cashier's own last cash_ups.period_end, floored to the start of today
// (so a forgotten cash-up from days ago can't silently balloon the next
// shift's window, while still supporting more than one shift per day).
package cashups

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gizmojunction/backend/internal/shifts"
)

type Repo struct {
	pool   *pgxpool.Pool
	shifts *shifts.Repo
}

func NewRepo(pool *pgxpool.Pool, shiftsRepo *shifts.Repo) *Repo {
	return &Repo{pool: pool, shifts: shiftsRepo}
}

type MethodTotal struct {
	PaymentMethod string  `json:"payment_method"`
	Count         int32   `json:"count"`
	Total         float64 `json:"total"`
}

type Summary struct {
	PeriodStart time.Time     `json:"period_start"`
	Methods     []MethodTotal `json:"methods"`
	OrderCount  int32         `json:"order_count"`
	CashTotal   float64       `json:"cash_total"`
}

type CashUp struct {
	ID           string          `db:"id" json:"id"`
	CashierID    string          `db:"cashier_id" json:"cashier_id"`
	PeriodStart  time.Time       `db:"period_start" json:"period_start"`
	PeriodEnd    time.Time       `db:"period_end" json:"period_end"`
	ExpectedCash float64         `db:"expected_cash" json:"expected_cash"`
	CountedCash  float64         `db:"counted_cash" json:"counted_cash"`
	Variance     float64         `db:"variance" json:"variance"`
	Breakdown    json.RawMessage `db:"breakdown" json:"breakdown"`
	Notes        *string         `db:"notes" json:"notes,omitempty"`
	CreatedAt    time.Time       `db:"created_at" json:"created_at"`
	CashierName  *string         `db:"cashier_name" json:"cashier_name,omitempty"`
}

// periodStart prefers a real, logged-in shift's started_at — a true bound,
// not a guess. Falls back to the old derived-window logic (since the last
// cash-up, floored to today) only when no open shift exists: an ADMIN
// account (which never gets shifts, see internal/auth's Login hook), or a
// CASHIER account that predates this feature.
func (r *Repo) periodStart(ctx context.Context, cashierID string) (time.Time, error) {
	if r.shifts != nil {
		if start, err := r.shifts.CurrentShiftStart(ctx, cashierID); err != nil {
			return time.Time{}, err
		} else if start != nil {
			return *start, nil
		}
	}

	var periodStart time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT GREATEST(
			COALESCE((SELECT max(period_end) FROM cash_ups WHERE cashier_id = $1), date_trunc('day', now())),
			date_trunc('day', now())
		)`, cashierID).Scan(&periodStart)
	return periodStart, err
}

func (r *Repo) shiftSummaryAt(ctx context.Context, cashierID string, periodStart, periodEnd time.Time) (Summary, error) {
	s := Summary{PeriodStart: periodStart}

	rows, err := r.pool.Query(ctx, `
		SELECT COALESCE(payment_method, 'unknown') AS payment_method, count(*), COALESCE(sum(total_amount), 0)::float8
		FROM orders
		WHERE served_by = $1 AND created_at >= $2 AND created_at <= $3
		GROUP BY payment_method
		ORDER BY payment_method`,
		cashierID, periodStart, periodEnd)
	if err != nil {
		return s, err
	}
	defer rows.Close()

	for rows.Next() {
		var m MethodTotal
		if err := rows.Scan(&m.PaymentMethod, &m.Count, &m.Total); err != nil {
			return s, err
		}
		s.Methods = append(s.Methods, m)
		s.OrderCount += m.Count
		if m.PaymentMethod == "cash" {
			s.CashTotal = m.Total
		}
	}
	if err := rows.Err(); err != nil {
		return s, err
	}
	if s.Methods == nil {
		s.Methods = []MethodTotal{}
	}
	return s, nil
}

// ShiftSummary previews the current, still-open shift as of now — what the
// cash-up page shows before the cashier submits their count.
func (r *Repo) ShiftSummary(ctx context.Context, cashierID string) (Summary, error) {
	periodStart, err := r.periodStart(ctx, cashierID)
	if err != nil {
		return Summary{}, err
	}
	return r.shiftSummaryAt(ctx, cashierID, periodStart, time.Now())
}

// SubmitCashUp recomputes the shift summary as of now, records the count,
// and closes the shift — the next ShiftSummary call for this cashier starts
// fresh from this cash-up's period_end.
func (r *Repo) SubmitCashUp(ctx context.Context, cashierID string, countedCash float64, notes string) (CashUp, error) {
	periodStart, err := r.periodStart(ctx, cashierID)
	if err != nil {
		return CashUp{}, err
	}
	periodEnd := time.Now()

	summary, err := r.shiftSummaryAt(ctx, cashierID, periodStart, periodEnd)
	if err != nil {
		return CashUp{}, err
	}

	breakdown := map[string]any{"order_count": summary.OrderCount}
	for _, m := range summary.Methods {
		breakdown[m.PaymentMethod] = m.Total
	}
	breakdownJSON, err := json.Marshal(breakdown)
	if err != nil {
		return CashUp{}, err
	}

	variance := countedCash - summary.CashTotal

	var c CashUp
	err = r.pool.QueryRow(ctx, `
		INSERT INTO cash_ups (cashier_id, period_start, period_end, expected_cash, counted_cash, variance, breakdown, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''))
		RETURNING id::text, cashier_id::text, period_start, period_end, expected_cash::float8, counted_cash::float8, variance::float8, breakdown, notes, created_at`,
		cashierID, periodStart, periodEnd, summary.CashTotal, countedCash, variance, breakdownJSON, notes,
	).Scan(&c.ID, &c.CashierID, &c.PeriodStart, &c.PeriodEnd, &c.ExpectedCash, &c.CountedCash, &c.Variance, &c.Breakdown, &c.Notes, &c.CreatedAt)
	if err != nil {
		return CashUp{}, err
	}

	// Submitting a cash-up is one of the two ways a shift ends (the other
	// being an explicit logout, handled in internal/auth) — a no-op if this
	// cashier has no open shift (e.g. an ADMIN using cash-up).
	if r.shifts != nil {
		_ = r.shifts.CloseOpenForCashier(ctx, cashierID, "cash_up")
	}

	return c, nil
}

// ListCashUps returns all cash-ups when cashierID is nil (admin view), or
// just one cashier's own history otherwise.
func (r *Repo) ListCashUps(ctx context.Context, cashierID *string) ([]CashUp, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT cu.id::text, cu.cashier_id::text, cu.period_start, cu.period_end,
			cu.expected_cash::float8, cu.counted_cash::float8, cu.variance::float8,
			cu.breakdown, cu.notes, cu.created_at, p.full_name
		FROM cash_ups cu
		LEFT JOIN profiles p ON p.id = cu.cashier_id
		WHERE $1::uuid IS NULL OR cu.cashier_id = $1
		ORDER BY cu.created_at DESC`,
		cashierID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []CashUp{}
	for rows.Next() {
		var c CashUp
		if err := rows.Scan(&c.ID, &c.CashierID, &c.PeriodStart, &c.PeriodEnd, &c.ExpectedCash, &c.CountedCash, &c.Variance, &c.Breakdown, &c.Notes, &c.CreatedAt, &c.CashierName); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
}
