// Package returns covers returns/refunds/exchanges — ADMIN_RETAIL_OPS_REVIEW.md
// §8.2. A return is a fact about an order, not a replacement order_status
// (that enum stays untouched — CANCELLED already means something different,
// pre-fulfillment). Stock reversal is a fresh, independent per-line
// increment, deliberately not reusing orders.Repo's all-or-nothing
// stock_decremented flag, which can't represent "restock 2 of 5 returned."
//
// Refund payout (cash/M-Pesa) is record-only by design: there is no
// Safaricom B2C integration in this codebase (only STK push, which is
// inbound), so the actual money movement happens outside the system and
// this just tracks method/amount/reference for reconciliation. Likewise,
// KRA credit-note submission is tracked (credit_note_status) rather than
// auto-submitted — the OSCU client itself is pre-certification scaffold
// (see taxetims/client.go's own top-of-file caveat), so this avoids writing
// a second speculative payload builder against an unverified integration.
package returns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type ReturnItemInput struct {
	OrderItemID string
	Quantity    int32
	Condition   string // "restock" | "damaged"
}

// NewReturn is CreateReturn's input — named distinctly from the handler's
// huma input type to avoid a naming collision within the package.
type NewReturn struct {
	OrderID         string
	Items           []ReturnItemInput
	Reason          string
	RefundMethod    string
	RefundAmount    float64
	RefundReference string
}

type ReturnItem struct {
	ID          string  `json:"id"`
	OrderItemID string  `json:"order_item_id"`
	Quantity    int32   `json:"quantity"`
	Condition   string  `json:"condition"`
	ProductName string  `json:"product_name,omitempty"`
	UnitPrice   float64 `json:"unit_price,omitempty"`
}

type Return struct {
	ID                      string          `db:"id" json:"id"`
	OrderID                 string          `db:"order_id" json:"order_id"`
	Reason                  *string         `db:"reason" json:"reason,omitempty"`
	RefundMethod            string          `db:"refund_method" json:"refund_method"`
	RefundAmount            float64         `db:"refund_amount" json:"refund_amount"`
	RefundReference         *string         `db:"refund_reference" json:"refund_reference,omitempty"`
	OriginalCUInvoiceNumber *string         `db:"original_cu_invoice_number" json:"original_cu_invoice_number,omitempty"`
	CreditNoteStatus        string          `db:"credit_note_status" json:"credit_note_status"`
	CreditNoteReference     *string         `db:"credit_note_reference" json:"credit_note_reference,omitempty"`
	CreatedAt               time.Time       `db:"created_at" json:"created_at"`
	OrderCreatedAt          time.Time       `db:"order_created_at" json:"order_created_at"`
	ShippingAddress         json.RawMessage `db:"shipping_address" json:"shipping_address,omitempty"`
	PaymentMethod           *string         `db:"payment_method" json:"payment_method,omitempty"`
	Items                   []ReturnItem    `db:"-" json:"items"`
}

const returnColumns = `rt.id::text, rt.order_id::text, rt.reason, rt.refund_method, rt.refund_amount,
	rt.refund_reference, rt.original_cu_invoice_number, rt.credit_note_status, rt.credit_note_reference,
	rt.created_at, o.created_at AS order_created_at, o.shipping_address, o.payment_method`

// CreateReturn validates every line against what's actually left to return
// (purchased quantity minus whatever prior returns already claimed), then
// inserts the return + its items in one transaction and restocks any
// condition="restock" lines. condition="damaged" touches no stock — a
// write-off.
func (r *Repo) CreateReturn(ctx context.Context, in NewReturn) (string, error) {
	if len(in.Items) == 0 {
		return "", fmt.Errorf("at least one item is required")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	for _, item := range in.Items {
		if item.Condition != "restock" && item.Condition != "damaged" {
			return "", fmt.Errorf("invalid condition %q for item %s", item.Condition, item.OrderItemID)
		}

		var purchasedQty int32
		var belongsToOrder bool
		err := tx.QueryRow(ctx, `
			SELECT quantity, order_id = $2 FROM order_items WHERE id = $1 FOR UPDATE`,
			item.OrderItemID, in.OrderID).Scan(&purchasedQty, &belongsToOrder)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", fmt.Errorf("order item %s not found", item.OrderItemID)
			}
			return "", err
		}
		if !belongsToOrder {
			return "", fmt.Errorf("order item %s does not belong to order %s", item.OrderItemID, in.OrderID)
		}

		var alreadyReturned int32
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(quantity), 0) FROM return_items WHERE order_item_id = $1`,
			item.OrderItemID).Scan(&alreadyReturned); err != nil {
			return "", err
		}
		if alreadyReturned+item.Quantity > purchasedQty {
			return "", fmt.Errorf("cannot return %d of item %s — only %d remaining (purchased %d, already returned %d)",
				item.Quantity, item.OrderItemID, purchasedQty-alreadyReturned, purchasedQty, alreadyReturned)
		}
	}

	// A return is flagged credit_note_status="required" only when the
	// original order actually reached an ISSUED KRA tax invoice — orders.
	// tax_status is denormalized by taxetims.Repo.MarkIssued for exactly
	// this kind of cheap check without joining tax_invoices on every read.
	var taxStatus *string
	if err := tx.QueryRow(ctx, `SELECT tax_status FROM orders WHERE id = $1`, in.OrderID).Scan(&taxStatus); err != nil {
		return "", fmt.Errorf("load order: %w", err)
	}

	creditNoteStatus := "not_applicable"
	var originalCU *string
	if taxStatus != nil && *taxStatus == "ISSUED" {
		creditNoteStatus = "required"
		if err := tx.QueryRow(ctx, `SELECT cu_invoice_number FROM tax_invoices WHERE order_id = $1`, in.OrderID).
			Scan(&originalCU); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("load tax invoice: %w", err)
		}
	}

	var returnID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO returns (order_id, reason, refund_method, refund_amount, refund_reference, original_cu_invoice_number, credit_note_status)
		VALUES ($1, NULLIF($2, ''), $3, $4, NULLIF($5, ''), $6, $7)
		RETURNING id::text`,
		in.OrderID, in.Reason, in.RefundMethod, in.RefundAmount, in.RefundReference, originalCU, creditNoteStatus,
	).Scan(&returnID); err != nil {
		return "", fmt.Errorf("insert return: %w", err)
	}

	for _, item := range in.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO return_items (return_id, order_item_id, quantity, condition)
			VALUES ($1, $2, $3, $4)`, returnID, item.OrderItemID, item.Quantity, item.Condition); err != nil {
			return "", fmt.Errorf("insert return item: %w", err)
		}
		if item.Condition == "restock" {
			if _, err := tx.Exec(ctx, `
				UPDATE products SET stock_quantity = stock_quantity + $2
				FROM order_items oi
				WHERE products.id = oi.product_id AND oi.id = $1`, item.OrderItemID, item.Quantity); err != nil {
				return "", fmt.Errorf("restock item %s: %w", item.OrderItemID, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return returnID, nil
}

func (r *Repo) ListReturns(ctx context.Context) ([]Return, error) {
	return r.listReturns(ctx, "")
}

func (r *Repo) ListReturnsForOrder(ctx context.Context, orderID string) ([]Return, error) {
	return r.listReturns(ctx, "WHERE rt.order_id = $1", orderID)
}

func (r *Repo) listReturns(ctx context.Context, whereClause string, args ...any) ([]Return, error) {
	query := `SELECT ` + returnColumns + `
		FROM returns rt JOIN orders o ON o.id = rt.order_id
		` + whereClause + `
		ORDER BY rt.created_at DESC`
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[Return])
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []Return{}
	}
	if err := r.attachReturnItems(ctx, list); err != nil {
		return nil, err
	}
	return list, nil
}

type returnItemRow struct {
	ID          string  `db:"id"`
	ReturnID    string  `db:"return_id"`
	OrderItemID string  `db:"order_item_id"`
	Quantity    int32   `db:"quantity"`
	Condition   string  `db:"condition"`
	ProductName string  `db:"product_name"`
	UnitPrice   float64 `db:"unit_price"`
}

func (r *Repo) attachReturnItems(ctx context.Context, list []Return) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	for i, rt := range list {
		ids[i] = rt.ID
	}

	rows, err := r.pool.Query(ctx, `
		SELECT ri.id::text, ri.return_id::text, ri.order_item_id::text, ri.quantity, ri.condition,
			COALESCE(p.name, 'Deleted product') AS product_name, oi.unit_price
		FROM return_items ri
		JOIN order_items oi ON oi.id = ri.order_item_id
		LEFT JOIN products p ON p.id = oi.product_id
		WHERE ri.return_id = ANY($1)
		ORDER BY ri.created_at`, ids)
	if err != nil {
		return err
	}
	itemRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[returnItemRow])
	if err != nil {
		return err
	}

	byReturn := make(map[string][]ReturnItem, len(list))
	for _, it := range itemRows {
		byReturn[it.ReturnID] = append(byReturn[it.ReturnID], ReturnItem{
			ID: it.ID, OrderItemID: it.OrderItemID, Quantity: it.Quantity,
			Condition: it.Condition, ProductName: it.ProductName, UnitPrice: it.UnitPrice,
		})
	}
	for i := range list {
		list[i].Items = byReturn[list[i].ID]
		if list[i].Items == nil {
			list[i].Items = []ReturnItem{}
		}
	}
	return nil
}

// ReturnedQuantities maps order_item_id -> total quantity already returned
// across all prior returns, so the frontend can cap each line's returnable
// quantity at (purchased - already returned).
func (r *Repo) ReturnedQuantities(ctx context.Context, orderID string) (map[string]int32, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT oi.id::text, COALESCE(SUM(ri.quantity), 0)::int
		FROM order_items oi
		LEFT JOIN return_items ri ON ri.order_item_id = oi.id
		WHERE oi.order_id = $1
		GROUP BY oi.id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int32)
	for rows.Next() {
		var id string
		var qty int32
		if err := rows.Scan(&id, &qty); err != nil {
			return nil, err
		}
		result[id] = qty
	}
	return result, rows.Err()
}

// MarkCreditNoteIssued records that an admin has manually filed the KRA
// credit note (via the OSCU portal/device directly) — see the package
// comment for why this isn't an automated submission yet.
func (r *Repo) MarkCreditNoteIssued(ctx context.Context, returnID, reference string) error {
	ct, err := r.pool.Exec(ctx, `
		UPDATE returns SET credit_note_status = 'issued', credit_note_reference = NULLIF($2, '')
		WHERE id = $1`, returnID, reference)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
