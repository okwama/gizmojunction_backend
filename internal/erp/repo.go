package erp

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo holds the ERP page's queries. The stats aggregation happens in SQL —
// the page previously fetched the entire products table client-side just to
// count SKUs and sum cost*stock.
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

const supplierColumns = `id::text, name, contact, email, phone, address, terms, created_at`

func (r *Repo) Overview(ctx context.Context) (Overview, error) {
	var o Overview

	err := r.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(SUM(COALESCE(cost_price, 0) * COALESCE(stock_quantity, 0)), 0)::float8
		FROM products`).Scan(&o.Stats.TotalSKUs, &o.Stats.TotalValue)
	if err != nil {
		return o, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id::text, sku, name, stock_quantity, COALESCE(updated_at, created_at, now()) AS updated_at
		FROM products
		ORDER BY updated_at DESC
		LIMIT 10`)
	if err != nil {
		return o, err
	}
	if o.RecentProducts, err = pgx.CollectRows(rows, pgx.RowToStructByName[RecentProduct]); err != nil {
		return o, err
	}

	rows, err = r.pool.Query(ctx, `SELECT `+supplierColumns+` FROM suppliers ORDER BY name ASC`)
	if err != nil {
		return o, err
	}
	if o.Suppliers, err = pgx.CollectRows(rows, pgx.RowToStructByName[Supplier]); err != nil {
		return o, err
	}

	rows, err = r.pool.Query(ctx, `
		SELECT po.id::text, po.supplier_id::text, s.name AS supplier_name,
			COALESCE(po.status::text, 'DRAFT') AS status,
			COALESCE(po.total_amount, 0)::float8 AS total_amount,
			po.notes, po.created_at
		FROM purchase_orders po
		LEFT JOIN suppliers s ON s.id = po.supplier_id
		ORDER BY po.created_at DESC`)
	if err != nil {
		return o, err
	}
	if o.PurchaseOrders, err = pgx.CollectRows(rows, pgx.RowToStructByName[PurchaseOrder]); err != nil {
		return o, err
	}

	return o, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *Repo) CreateSupplier(ctx context.Context, name, contact, email, terms string) (Supplier, error) {
	rows, err := r.pool.Query(ctx, `
		INSERT INTO suppliers (name, contact, email, terms)
		VALUES ($1, $2, $3, $4)
		RETURNING `+supplierColumns,
		name, nullIfEmpty(contact), nullIfEmpty(email), nullIfEmpty(terms))
	if err != nil {
		return Supplier{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Supplier])
}

// CreatePurchaseOrder computes total_amount server-side from the submitted
// line items (same convention as quotes.CreateQuote) and writes both the
// purchase_orders row and its purchase_order_items in one transaction. A PO
// with zero items is rejected — there'd be nothing for a later GRN to
// receive against.
func (r *Repo) CreatePurchaseOrder(ctx context.Context, supplierID, notes string, items []POItemInput) (string, error) {
	if len(items) == 0 {
		return "", fmt.Errorf("at least one item is required")
	}

	var totalAmount float64
	for _, it := range items {
		if it.Quantity <= 0 {
			return "", fmt.Errorf("invalid quantity for item")
		}
		totalAmount += it.UnitCost * float64(it.Quantity)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO purchase_orders (supplier_id, notes, total_amount)
		VALUES ($1::uuid, $2, $3)
		RETURNING id::text`,
		supplierID, nullIfEmpty(notes), totalAmount).Scan(&id); err != nil {
		return "", fmt.Errorf("insert purchase order: %w", err)
	}

	for _, it := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO purchase_order_items (purchase_order_id, product_id, quantity, unit_cost)
			VALUES ($1, $2, $3, $4)`,
			id, it.ProductID, it.Quantity, it.UnitCost); err != nil {
			return "", fmt.Errorf("insert purchase order item: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// GetPurchaseOrder loads a PO's header and line items (with received
// quantities) for the receive page.
func (r *Repo) GetPurchaseOrder(ctx context.Context, id string) (PurchaseOrderDetail, error) {
	var d PurchaseOrderDetail
	err := r.pool.QueryRow(ctx, `
		SELECT po.id::text, po.supplier_id::text, s.name,
			COALESCE(po.status::text, 'DRAFT'), COALESCE(po.total_amount, 0)::float8,
			po.notes, po.created_at
		FROM purchase_orders po
		LEFT JOIN suppliers s ON s.id = po.supplier_id
		WHERE po.id = $1`,
		id).Scan(&d.ID, &d.SupplierID, &d.SupplierName, &d.Status, &d.TotalAmount, &d.Notes, &d.CreatedAt)
	if err != nil {
		return d, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT poi.id::text, poi.product_id::text, COALESCE(p.name, 'Unknown') AS name,
			COALESCE(p.sku, 'N/A') AS sku, poi.quantity, poi.unit_cost::float8 AS unit_cost,
			poi.received_quantity
		FROM purchase_order_items poi
		LEFT JOIN products p ON p.id = poi.product_id
		WHERE poi.purchase_order_id = $1
		ORDER BY poi.id`,
		id)
	if err != nil {
		return d, err
	}
	d.Items, err = pgx.CollectRows(rows, pgx.RowToStructByName[PurchaseOrderItemDetail])
	if err != nil {
		return d, err
	}
	if d.Items == nil {
		d.Items = []PurchaseOrderItemDetail{}
	}
	return d, nil
}

// ReceiveItems records goods physically received against a PO's line items,
// incrementing stock for each — mirrors returns.Repo's per-line, independent
// stock adjustment rather than reusing orders.Repo's all-or-nothing
// stock_decremented flag. Once every line on the PO has received_quantity >=
// quantity, the PO status flips to RECEIVED.
func (r *Repo) ReceiveItems(ctx context.Context, poID string, items []ReceivedItemInput, notes string) error {
	if len(items) == 0 {
		return fmt.Errorf("at least one item is required")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, it := range items {
		if it.QuantityReceived <= 0 {
			return fmt.Errorf("quantity received must be positive")
		}

		var quantity, receivedQuantity int32
		var productID *string
		if err := tx.QueryRow(ctx, `
			SELECT quantity, received_quantity, product_id::text
			FROM purchase_order_items WHERE id = $1 AND purchase_order_id = $2 FOR UPDATE`,
			it.PurchaseOrderItemID, poID).Scan(&quantity, &receivedQuantity, &productID); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("purchase order item %s not found on this order", it.PurchaseOrderItemID)
			}
			return err
		}

		remaining := quantity - receivedQuantity
		if it.QuantityReceived > remaining {
			return fmt.Errorf("cannot receive %d, only %d remaining", it.QuantityReceived, remaining)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE purchase_order_items SET received_quantity = received_quantity + $1 WHERE id = $2`,
			it.QuantityReceived, it.PurchaseOrderItemID); err != nil {
			return fmt.Errorf("update received quantity: %w", err)
		}

		if productID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE products SET stock_quantity = stock_quantity + $1 WHERE id = $2`,
				it.QuantityReceived, *productID); err != nil {
				return fmt.Errorf("increment stock: %w", err)
			}
		}
	}

	if notes != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE purchase_orders
			SET notes = COALESCE(notes || E'\n', '') || 'Received: ' || $2
			WHERE id = $1`, poID, notes); err != nil {
			return fmt.Errorf("append receiving notes: %w", err)
		}
	}

	var remainingLines int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM purchase_order_items WHERE purchase_order_id = $1 AND received_quantity < quantity`,
		poID).Scan(&remainingLines); err != nil {
		return fmt.Errorf("check remaining lines: %w", err)
	}
	if remainingLines == 0 {
		if _, err := tx.Exec(ctx, `UPDATE purchase_orders SET status = 'RECEIVED' WHERE id = $1`, poID); err != nil {
			return fmt.Errorf("mark purchase order received: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (r *Repo) PurchaseOrderForLPO(ctx context.Context, id string) (lpoData, error) {
	var d lpoData
	err := r.pool.QueryRow(ctx, `
		SELECT po.id::text, po.created_at, COALESCE(po.total_amount, 0)::float8,
			COALESCE(s.name, 'N/A'), COALESCE(s.email, ''), COALESCE(s.phone, '')
		FROM purchase_orders po
		LEFT JOIN suppliers s ON s.id = po.supplier_id
		WHERE po.id = $1`,
		id).Scan(&d.ID, &d.CreatedAt, &d.TotalAmount, &d.SupplierName, &d.SupplierEmail, &d.SupplierPhone)
	if err != nil {
		return d, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT COALESCE(p.name, 'Unknown') AS name, COALESCE(p.sku, 'N/A') AS sku,
			poi.quantity, poi.unit_cost::float8 AS unit_cost
		FROM purchase_order_items poi
		LEFT JOIN products p ON p.id = poi.product_id
		WHERE poi.purchase_order_id = $1`,
		id)
	if err != nil {
		return d, err
	}
	d.Items, err = pgx.CollectRows(rows, pgx.RowToStructByName[lpoItem])
	return d, err
}
