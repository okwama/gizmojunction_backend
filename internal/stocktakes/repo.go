// Package stocktakes covers ADMIN_RETAIL_OPS_REVIEW.md's cycle-count
// backlog item: pick a category (or the whole catalog), snapshot expected
// quantities, let staff enter actual counts, and post stock adjustments
// with a reason for any line that doesn't match.
//
// A DB trigger (log_stock_movement) already logs every products.stock_quantity
// change into stock_movements as a generic 'adjustment' row — the same
// precedent Returns and GRN follow. This package doesn't write its own
// stock_movements entries; the real audit detail (expected/counted/reason
// per line) lives in stock_take_items instead.
package stocktakes

import (
	"context"
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

type CountInput struct {
	StockTakeItemID string
	CountedQuantity int32
	Reason          string
}

type StockTakeItem struct {
	ID               string  `db:"id" json:"id"`
	ProductID        string  `db:"product_id" json:"product_id"`
	Name             string  `db:"name" json:"name"`
	SKU              string  `db:"sku" json:"sku"`
	ExpectedQuantity int32   `db:"expected_quantity" json:"expected_quantity"`
	CountedQuantity  *int32  `db:"counted_quantity" json:"counted_quantity,omitempty"`
	Reason           *string `db:"reason" json:"reason,omitempty"`
	Variance         *int32  `db:"-" json:"variance,omitempty"`
}

type StockTakeDetail struct {
	ID           string          `json:"id"`
	CategoryID   *string         `json:"category_id,omitempty"`
	CategoryName *string         `json:"category_name,omitempty"`
	Status       string          `json:"status"`
	CreatedAt    time.Time       `json:"created_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
	Items        []StockTakeItem `json:"items"`
}

type StockTake struct {
	ID           string     `db:"id" json:"id"`
	CategoryID   *string    `db:"category_id" json:"category_id,omitempty"`
	CategoryName *string    `db:"category_name" json:"category_name,omitempty"`
	Status       string     `db:"status" json:"status"`
	ItemCount    int32      `db:"item_count" json:"item_count"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	CompletedAt  *time.Time `db:"completed_at" json:"completed_at,omitempty"`
}

// StartStockTake snapshots every product matching categoryID (or the whole
// catalog when nil) into stock_take_items with expected_quantity = the
// current stock_quantity. Rejects a category with zero products — nothing
// to count.
func (r *Repo) StartStockTake(ctx context.Context, categoryID *string) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO stock_takes (category_id) VALUES ($1) RETURNING id::text`,
		categoryID).Scan(&id); err != nil {
		return "", fmt.Errorf("insert stock take: %w", err)
	}

	ct, err := tx.Exec(ctx, `
		INSERT INTO stock_take_items (stock_take_id, product_id, expected_quantity)
		SELECT $1, id, COALESCE(stock_quantity, 0)
		FROM products
		WHERE $2::uuid IS NULL OR category_id = $2`,
		id, categoryID)
	if err != nil {
		return "", fmt.Errorf("snapshot products: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return "", fmt.Errorf("no products found for this category")
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

func (r *Repo) ListStockTakes(ctx context.Context) ([]StockTake, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT st.id::text, st.category_id::text, c.name AS category_name,
			st.status, count(sti.id) AS item_count, st.created_at, st.completed_at
		FROM stock_takes st
		LEFT JOIN categories c ON c.id = st.category_id
		LEFT JOIN stock_take_items sti ON sti.stock_take_id = st.id
		GROUP BY st.id, c.name
		ORDER BY st.created_at DESC`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[StockTake])
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []StockTake{}
	}
	return list, nil
}

func (r *Repo) GetStockTake(ctx context.Context, id string) (StockTakeDetail, error) {
	var d StockTakeDetail
	if err := r.pool.QueryRow(ctx, `
		SELECT st.id::text, st.category_id::text, c.name, st.status, st.created_at, st.completed_at
		FROM stock_takes st
		LEFT JOIN categories c ON c.id = st.category_id
		WHERE st.id = $1`,
		id).Scan(&d.ID, &d.CategoryID, &d.CategoryName, &d.Status, &d.CreatedAt, &d.CompletedAt); err != nil {
		return d, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT sti.id::text, sti.product_id::text, COALESCE(p.name, 'Unknown') AS name,
			COALESCE(p.sku, 'N/A') AS sku, sti.expected_quantity, sti.counted_quantity, sti.reason
		FROM stock_take_items sti
		LEFT JOIN products p ON p.id = sti.product_id
		WHERE sti.stock_take_id = $1
		ORDER BY p.name`,
		id)
	if err != nil {
		return d, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[StockTakeItem])
	if err != nil {
		return d, err
	}
	for i := range items {
		if items[i].CountedQuantity != nil {
			v := *items[i].CountedQuantity - items[i].ExpectedQuantity
			items[i].Variance = &v
		}
	}
	d.Items = items
	if d.Items == nil {
		d.Items = []StockTakeItem{}
	}
	return d, nil
}

// SubmitCount records the physically-counted quantity for each submitted
// line, requiring a reason whenever the count differs from what was
// expected, adjusts product stock to the counted value for those lines,
// and marks the whole stock take completed. Lines not present in counts
// are left untouched (not yet counted).
func (r *Repo) SubmitCount(ctx context.Context, stockTakeID string, counts []CountInput) error {
	if len(counts) == 0 {
		return fmt.Errorf("at least one counted item is required")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, c := range counts {
		var productID string
		var expected int32
		if err := tx.QueryRow(ctx, `
			SELECT product_id::text, expected_quantity FROM stock_take_items
			WHERE id = $1 AND stock_take_id = $2 FOR UPDATE`,
			c.StockTakeItemID, stockTakeID).Scan(&productID, &expected); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("stock take item %s not found on this stock take", c.StockTakeItemID)
			}
			return err
		}

		if c.CountedQuantity != expected && c.Reason == "" {
			return fmt.Errorf("a reason is required when the counted quantity differs from expected")
		}

		if _, err := tx.Exec(ctx, `
			UPDATE stock_take_items SET counted_quantity = $1, reason = NULLIF($2, '') WHERE id = $3`,
			c.CountedQuantity, c.Reason, c.StockTakeItemID); err != nil {
			return fmt.Errorf("update stock take item: %w", err)
		}

		if c.CountedQuantity != expected {
			if _, err := tx.Exec(ctx, `
				UPDATE products SET stock_quantity = $1 WHERE id = $2`,
				c.CountedQuantity, productID); err != nil {
				return fmt.Errorf("adjust stock: %w", err)
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE stock_takes SET status = 'completed', completed_at = now() WHERE id = $1`,
		stockTakeID); err != nil {
		return fmt.Errorf("mark stock take completed: %w", err)
	}

	return tx.Commit(ctx)
}
