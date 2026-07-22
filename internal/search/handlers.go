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
	Brand    string `query:"brand" doc:"brand slug or name, optional — may be used without q to browse a brand"`
}

// relevanceFloor drops near-zero matches (e.g. a single shared trigram)
// rather than returning the entire catalog ranked by noise.
const relevanceFloor = 0.05

// The brand text column on products is unreliable (imports populate
// brand_id, not the denormalized name), so both filtering and the returned
// brand field go through the brands join.
const searchSelect = `
	SELECT p.id::text, p.name, p.sku,
	       COALESCE(NULLIF(p.brand, ''), b.name) AS brand,
	       p.price::float8, p.old_price::float8, p.sale_price::float8,
	       p.image_url, p.stock_quantity, p.rating::float8, p.review_count, p.is_featured, p.category_id::text
	FROM products p
	LEFT JOIN categories c ON c.id = p.category_id
	LEFT JOIN brands b ON b.id = p.brand_id
	WHERE p.is_published = true
	  AND ($1 = '' OR c.slug = $1)
	  AND ($2 = '' OR lower(b.slug) = lower($2) OR lower(b.name) = lower($2) OR lower(p.brand) = lower($2))`

func (h *handlers) Search(ctx context.Context, input *SearchInput) (*struct{ Body []catalog.ProductSummary }, error) {
	if input.Q == "" && input.Brand == "" {
		return &struct{ Body []catalog.ProductSummary }{Body: []catalog.ProductSummary{}}, nil
	}

	var rows pgx.Rows
	var err error
	if input.Q == "" {
		// Brand-browse mode: no text query to rank by, list the brand's
		// products alphabetically.
		rows, err = h.pool.Query(ctx, searchSelect+`
			ORDER BY p.name ASC
			LIMIT $3`,
			input.Category, input.Brand, input.Limit)
	} else {
		rows, err = h.pool.Query(ctx, searchSelect+`
			  AND GREATEST(
					ts_rank(
						to_tsvector('english', coalesce(p.description_plain, '') || ' ' || coalesce(p.summary_plain, '')),
						plainto_tsquery('english', $3)
					),
					similarity(p.name, $3),
					similarity(coalesce(p.brand, ''), $3),
					similarity(p.sku, $3)
				) > $4
			ORDER BY GREATEST(
					ts_rank(
						to_tsvector('english', coalesce(p.description_plain, '') || ' ' || coalesce(p.summary_plain, '')),
						plainto_tsquery('english', $3)
					),
					similarity(p.name, $3),
					similarity(coalesce(p.brand, ''), $3),
					similarity(p.sku, $3)
				) DESC
			LIMIT $5`,
			input.Category, input.Brand, input.Q, relevanceFloor, input.Limit)
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("search failed", err)
	}

	products, err := pgx.CollectRows(rows, pgx.RowToStructByName[catalog.ProductSummary])
	if err != nil {
		return nil, huma.Error500InternalServerError("search failed", err)
	}
	return &struct{ Body []catalog.ProductSummary }{Body: products}, nil
}
