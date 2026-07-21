// Package search implements product search using Postgres directly: the
// description_plain/summary_plain full-text GIN indexes already present in
// the baseline schema, plus pg_trgm trigram indexes (see migration
// 20260721010000_product_search_trgm) for typo-tolerant matching on the
// short fields (name/brand/sku). No external search service to deploy,
// pay for, or keep in sync with product writes.
package search

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gizmojunction/backend/internal/catalog"
)

// Register wires the public, unauthenticated search endpoint — the
// typo-tolerant replacement for the frontend's previous direct
// supabase.from('products')...ilike(...) calls (header suggestions +
// /search results page).
func Register(api huma.API, pool *pgxpool.Pool) {
	h := &handlers{pool: pool}
	huma.Register(api, huma.Operation{
		OperationID: "search-products",
		Method:      http.MethodGet,
		Path:        "/v1/search",
		Summary:     "Typo-tolerant product search",
	}, h.Search)
}

type handlers struct {
	pool *pgxpool.Pool
}

type SearchInput struct {
	Q        string `query:"q"`
	Limit    int64  `query:"limit" default:"24" minimum:"1" maximum:"100"`
	Category string `query:"category" doc:"category slug, optional"`
}

// relevanceFloor drops near-zero matches (e.g. a single shared trigram)
// rather than returning the entire catalog ranked by noise.
const relevanceFloor = 0.05

func (h *handlers) Search(ctx context.Context, input *SearchInput) (*struct{ Body []catalog.ProductSummary }, error) {
	if input.Q == "" {
		return &struct{ Body []catalog.ProductSummary }{Body: []catalog.ProductSummary{}}, nil
	}

	rows, err := h.pool.Query(ctx, `
		WITH matched AS (
			SELECT
				p.id, p.name, p.sku, p.brand, p.price, p.old_price, p.sale_price,
				p.image_url, p.stock_quantity, p.rating, p.review_count, p.is_featured, p.category_id,
				GREATEST(
					ts_rank(
						to_tsvector('english', coalesce(p.description_plain, '') || ' ' || coalesce(p.summary_plain, '')),
						plainto_tsquery('english', $1)
					),
					similarity(p.name, $1),
					similarity(coalesce(p.brand, ''), $1),
					similarity(p.sku, $1)
				) AS rank
			FROM products p
			LEFT JOIN categories c ON c.id = p.category_id
			WHERE p.is_published = true
			  AND ($3 = '' OR c.slug = $3)
		)
		SELECT id::text, name, sku, brand, price::float8, old_price::float8, sale_price::float8,
		       image_url, stock_quantity, rating::float8, review_count, is_featured, category_id::text
		FROM matched
		WHERE rank > $4
		ORDER BY rank DESC
		LIMIT $2`,
		input.Q, input.Limit, input.Category, relevanceFloor)
	if err != nil {
		return nil, huma.Error500InternalServerError("search failed", err)
	}

	products, err := pgx.CollectRows(rows, pgx.RowToStructByName[catalog.ProductSummary])
	if err != nil {
		return nil, huma.Error500InternalServerError("search failed", err)
	}
	return &struct{ Body []catalog.ProductSummary }{Body: products}, nil
}
