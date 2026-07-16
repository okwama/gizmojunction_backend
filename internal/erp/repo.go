package erp

import (
	"context"

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

func (r *Repo) CreatePurchaseOrder(ctx context.Context, supplierID, notes string) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO purchase_orders (supplier_id, notes)
		VALUES ($1::uuid, $2)
		RETURNING id::text`,
		supplierID, nullIfEmpty(notes)).Scan(&id)
	return id, err
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
