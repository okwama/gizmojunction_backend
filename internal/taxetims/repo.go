package taxetims

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAlreadyExists is returned by InsertPending when a tax_invoices row for
// the order already exists — the caller should treat this as success
// (idempotent enqueue), not an error.
var ErrAlreadyExists = errors.New("tax invoice already exists for this order")

type Repo struct {
	// Pool is a connection to Supabase's Postgres, not Neon — orders and
	// tax_invoices both still live there since Phase 5 (moving CRUD into
	// Neon) hasn't happened yet. See the migration plan's Phase 4.6 note.
	Pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{Pool: pool}
}

// LoadOrder fetches the order fields needed to build an OSCU payload.
func (r *Repo) LoadOrder(ctx context.Context, orderID string) (OrderForInvoice, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, total_amount::float8, payment_method, kra_pin, created_at
		FROM orders WHERE id = $1
	`, orderID)
	if err != nil {
		return OrderForInvoice{}, fmt.Errorf("query order: %w", err)
	}
	order, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[OrderForInvoice])
	if err != nil {
		return OrderForInvoice{}, fmt.Errorf("load order %s: %w", orderID, err)
	}
	return order, nil
}

// LoadOrderLines fetches order_items joined with product tax fields.
func (r *Repo) LoadOrderLines(ctx context.Context, orderID string) ([]OrderLine, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT p.name, p.sku, oi.quantity, oi.unit_price::float8, p.tax_class, p.tax_rate::float8
		FROM order_items oi JOIN products p ON p.id = oi.product_id
		WHERE oi.order_id = $1
	`, orderID)
	if err != nil {
		return nil, fmt.Errorf("query order items: %w", err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowToStructByName[OrderLine])
	if err != nil {
		return nil, fmt.Errorf("scan order items: %w", err)
	}
	return lines, nil
}

// InsertPending creates a PENDING tax_invoices row for order_id, idempotent
// on the table's UNIQUE(order_id) constraint. Returns ErrAlreadyExists (not
// a hard error) if one already exists, so callers can treat "an attempt is
// already in flight for this order" as success.
func (r *Repo) InsertPending(ctx context.Context, orderID, taxpayerPIN string, totalAmount, taxAmount float64) (string, error) {
	var id string
	err := r.Pool.QueryRow(ctx, `
		INSERT INTO tax_invoices (order_id, status, taxpayer_pin, total_amount, tax_amount)
		VALUES ($1, 'PENDING', $2, $3, $4)
		RETURNING id
	`, orderID, taxpayerPIN, totalAmount, taxAmount).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			var existingID string
			lookupErr := r.Pool.QueryRow(ctx, `SELECT id FROM tax_invoices WHERE order_id = $1`, orderID).Scan(&existingID)
			if lookupErr != nil {
				return "", fmt.Errorf("lookup existing tax invoice: %w", lookupErr)
			}
			return existingID, ErrAlreadyExists
		}
		return "", fmt.Errorf("insert pending tax invoice: %w", err)
	}
	return id, nil
}

// GetByID loads a tax_invoices row for resubmission (manual retry).
func (r *Repo) GetByID(ctx context.Context, id string) (TaxInvoice, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, order_id, status, taxpayer_pin, buyer_pin, cu_id, cu_invoice_number,
		       receipt_signature, internal_data, receipt_number, total_amount::float8,
		       tax_amount::float8, request_payload, response_payload, attempt_count,
		       last_attempt_at, issued_at, error_message, receipt_pdf_path
		FROM tax_invoices WHERE id = $1
	`, id)
	if err != nil {
		return TaxInvoice{}, fmt.Errorf("query tax invoice: %w", err)
	}
	inv, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[TaxInvoice])
	if err != nil {
		return TaxInvoice{}, fmt.Errorf("load tax invoice %s: %w", id, err)
	}
	return inv, nil
}

// GetIDByOrderID resolves an order's tax_invoices row id, if any.
func (r *Repo) GetIDByOrderID(ctx context.Context, orderID string) (string, error) {
	var id string
	err := r.Pool.QueryRow(ctx, `SELECT id FROM tax_invoices WHERE order_id = $1`, orderID).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

// ResetForRetry clears a FAILED/FAILED_FINAL invoice back to PENDING with
// attempt_count reset, so the worker treats it as a fresh submission.
func (r *Repo) ResetForRetry(ctx context.Context, id string) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE tax_invoices
		SET status = 'PENDING', attempt_count = 0, error_message = NULL
		WHERE id = $1
	`, id)
	return err
}

// MarkAttempt increments attempt_count and stamps last_attempt_at before an
// OSCU submission attempt — kept separate from MarkIssued/MarkFailed so the
// count reflects every attempt, including one that times out mid-flight.
func (r *Repo) MarkAttempt(ctx context.Context, id string) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE tax_invoices SET attempt_count = attempt_count + 1, last_attempt_at = now()
		WHERE id = $1
	`, id)
	return err
}

// MarkIssued records a successful OSCU response and syncs the denormalized
// orders.tax_status/tax_invoice_id columns in the same transaction.
func (r *Repo) MarkIssued(ctx context.Context, id, orderID string, cuInvoiceNumber, receiptSignature, internalData, cuID string, requestPayload, responsePayload []byte) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE tax_invoices
		SET status = 'ISSUED', cu_invoice_number = $2, receipt_signature = $3, internal_data = $4,
		    cu_id = $5, request_payload = $6, response_payload = $7, issued_at = now(), error_message = NULL
		WHERE id = $1
	`, id, cuInvoiceNumber, receiptSignature, internalData, cuID, requestPayload, responsePayload); err != nil {
		return fmt.Errorf("mark issued: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE orders SET tax_status = 'ISSUED', tax_invoice_id = $2, tax_error_message = NULL
		WHERE id = $1
	`, orderID, id); err != nil {
		return fmt.Errorf("sync order tax_status: %w", err)
	}

	return tx.Commit(ctx)
}

// MarkFailed records a retryable failure (river will retry the job itself;
// this just keeps the row's status/error_message in sync for the admin UI).
func (r *Repo) MarkFailed(ctx context.Context, id, orderID, errMsg string) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE tax_invoices SET status = 'FAILED', error_message = $2 WHERE id = $1`, id, errMsg); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE orders SET tax_status = 'FAILED', tax_attempts = tax_attempts + 1, tax_last_attempt_at = now(), tax_error_message = $2
		WHERE id = $1
	`, orderID, errMsg); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkFailedFinal records the terminal failure state after the last retry
// attempt is exhausted (FR-OSCU-24 in the BRD).
func (r *Repo) MarkFailedFinal(ctx context.Context, id, orderID, errMsg string) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE tax_invoices SET status = 'FAILED_FINAL', error_message = $2 WHERE id = $1`, id, errMsg); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE orders SET tax_status = 'FAILED_FINAL', tax_error_message = $2 WHERE id = $1
	`, orderID, errMsg); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetDeviceConfig returns the single-row OSCU device config, or
// pgx.ErrNoRows if the device hasn't been initialized yet.
func (r *Repo) GetDeviceConfig(ctx context.Context) (DeviceConfig, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT environment, tin, bhf_id, dvc_srl_no, cmc_key, sdc_id, mrc_no
		FROM kra_device_config WHERE id = 1
	`)
	if err != nil {
		return DeviceConfig{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[DeviceConfig])
}

// SaveDeviceConfig upserts the single-row device config after a successful
// initialization call.
func (r *Repo) SaveDeviceConfig(ctx context.Context, cfg DeviceConfig) error {
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO kra_device_config (id, environment, tin, bhf_id, dvc_srl_no, cmc_key, sdc_id, mrc_no, initialized_at, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, now(), now())
		ON CONFLICT (id) DO UPDATE SET
			environment = EXCLUDED.environment, tin = EXCLUDED.tin, bhf_id = EXCLUDED.bhf_id,
			dvc_srl_no = EXCLUDED.dvc_srl_no, cmc_key = EXCLUDED.cmc_key, sdc_id = EXCLUDED.sdc_id,
			mrc_no = EXCLUDED.mrc_no, updated_at = now()
	`, cfg.Environment, cfg.TIN, cfg.BhfID, cfg.DvcSrlNo, cfg.CMCKey, cfg.SdcID, cfg.MrcNo)
	return err
}
