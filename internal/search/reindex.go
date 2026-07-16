package search

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const productDocColumns = `p.id::text, p.name, p.sku, COALESCE(p.brand, '') AS brand, COALESCE(p.category_id::text, '') AS category_id, COALESCE(c.name, '') AS category_name, COALESCE(c.slug, '') AS category_slug, p.price::float8, COALESCE(p.old_price::float8, 0) AS old_price, COALESCE(p.sale_price::float8, 0) AS sale_price, COALESCE(p.image_url, '') AS image_url, p.stock_quantity, COALESCE(p.rating::float8, 0) AS rating, COALESCE(p.review_count, 0) AS review_count, p.is_featured, p.is_published`

const productDocFrom = `FROM products p LEFT JOIN categories c ON c.id = p.category_id`

// AllProductDocs pulls every product (published or not — IsPublished is a
// filterable attribute rather than a reason to omit a row) for a full
// reindex, driven by the admin "Reindex" endpoint.
func AllProductDocs(ctx context.Context, pool *pgxpool.Pool) ([]ProductDoc, error) {
	rows, err := pool.Query(ctx, `SELECT `+productDocColumns+` `+productDocFrom)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductDoc])
}

// ProductDocByID re-derives a single product's search document straight
// from Postgres (rather than accepting a caller-supplied partial doc) so a
// product save always reindexes with fresh rating/review_count aggregates
// too — Meilisearch's AddDocuments replaces the whole document, so a
// partial doc would silently zero those fields out on every edit.
func ProductDocByID(ctx context.Context, pool *pgxpool.Pool, id string) (*ProductDoc, error) {
	rows, err := pool.Query(ctx, `SELECT `+productDocColumns+` `+productDocFrom+` WHERE p.id = $1`, id)
	if err != nil {
		return nil, err
	}
	doc, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[ProductDoc])
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &doc, nil
}
