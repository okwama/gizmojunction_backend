// Package labels covers ADMIN_RETAIL_OPS_REVIEW.md's shelf-label backlog
// item: a simple barcode + name + price sheet for unlabeled arrivals (after
// GRN receiving) and shelf-edge relabeling (after a price change). Purely
// stateless — no new tables, just a PDF rendered on demand from the
// products already in the catalog.
package labels

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

type Product struct {
	ID        string   `db:"id"`
	Name      string   `db:"name"`
	SKU       string   `db:"sku"`
	Barcode   *string  `db:"barcode"`
	Price     float64  `db:"price"`
	SalePrice *float64 `db:"sale_price"`
}

// EffectivePrice mirrors the storefront's own sale_price-wins-if-set
// convention (same as POS's addToCart: product.sale_price || product.price).
func (p Product) EffectivePrice() float64 {
	if p.SalePrice != nil && *p.SalePrice > 0 {
		return *p.SalePrice
	}
	return p.Price
}

func (r *Repo) ProductsByIDs(ctx context.Context, ids []string) ([]Product, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, name, sku, barcode, COALESCE(price, 0)::float8 AS price, sale_price::float8
		FROM products WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[Product])
	if err != nil {
		return nil, err
	}
	return list, nil
}
