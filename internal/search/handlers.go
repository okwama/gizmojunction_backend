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
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

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

// SearchInput: Category/Brand (singular) are unchanged — Category is used by
// the header's live-search category dropdown, Brand is the "browse this
// brand" mode from MegaMenu/brands-index links (no query text). Categories/
// Brands (plural) are new multi-select sidebar refinements, deliberately
// separate params so neither existing singular caller is touched. Facets
// defaults false so the header's 6-item suggestion dropdown skips the extra
// aggregate queries entirely.
type SearchInput struct {
	Q          string  `query:"q"`
	Limit      int64   `query:"limit" default:"24" minimum:"1" maximum:"100"`
	Page       int64   `query:"page" default:"1" minimum:"1"`
	Category   string  `query:"category" doc:"category slug, optional — single-scope filter used by the header live-search dropdown"`
	Categories string  `query:"categories" doc:"comma-separated top-level department slugs — multi-select refinement"`
	Brand      string  `query:"brand" doc:"brand slug or name, optional — may be used without q to browse a brand"`
	Brands     string  `query:"brands" doc:"comma-separated brand names — multi-select refinement"`
	MinPrice   float64 `query:"min_price" minimum:"0"`
	MaxPrice   float64 `query:"max_price" minimum:"0"`
	Rating     int     `query:"rating" default:"0" minimum:"0" maximum:"4"`
	InStock    bool    `query:"in_stock" default:"false"`
	Sort       string  `query:"sort" default:"best_match" enum:"best_match,price_asc,price_desc,newest"`
	Facets     bool    `query:"facets" default:"false" doc:"compute faceted sidebar counts (3 extra aggregate queries) — the results page sets this, the header dropdown omits it"`
}

type SearchResponse struct {
	Products []catalog.ProductSummary `json:"products"`
	Facets   *catalog.Facets          `json:"facets,omitempty"`
	Total    int                      `json:"total"`
	Page     int64                    `json:"page"`
	Limit    int64                    `json:"limit"`
}

// relevanceFloor drops near-zero matches (e.g. a single shared trigram)
// rather than returning the entire catalog ranked by noise.
const relevanceFloor = 0.05

const relevanceExpr = `GREATEST(
	ts_rank(to_tsvector('english', coalesce(p.description_plain, '') || ' ' || coalesce(p.summary_plain, '')), plainto_tsquery('english', $9)),
	similarity(p.name, $9),
	similarity(coalesce(p.brand, ''), $9),
	similarity(p.sku, $9)
)`

// searchWhere is the WHERE clause shared by the main listing, count, and
// every facet query: is_published, the singular category/brand base scope
// (unchanged), and the four plain filters. $7 (brands) / $8 (category ids,
// resolved from Categories) are always present here — facet self-exclusion
// is achieved by passing a neutral value (nil/0/false) for the ONE
// dimension a given facet represents, not by varying this query text, so
// every query composed from it has the exact same placeholder shape.
const searchWhere = `
	p.is_published = true
	  AND ($1 = '' OR c.slug = $1)
	  AND ($2 = '' OR lower(b.slug) = lower($2) OR lower(b.name) = lower($2) OR lower(p.brand) = lower($2))
	  AND ($3 <= 0 OR p.price >= $3)
	  AND ($4 <= 0 OR p.price <= $4)
	  AND ($5 = 0 OR p.rating >= $5)
	  AND ($6 = false OR p.stock_quantity > 0)
	  AND (array_length($7::text[], 1) IS NULL OR COALESCE(NULLIF(p.brand,''), b.name) = ANY($7))
	  AND (array_length($8::uuid[], 1) IS NULL OR p.category_id = ANY($8))`

const searchFrom = `products p LEFT JOIN categories c ON c.id = p.category_id LEFT JOIN brands b ON b.id = p.brand_id`

// searchArgs holds every filter dimension used to build $1..$8 above, plus
// Q (only meaningful when non-empty — referenced as $9 by relevanceExpr).
type searchArgs struct {
	category, brand string
	minPrice        float64
	maxPrice        float64
	rating          int
	inStock         bool
	brands          []string
	categoryIDs     []string
	q               string
}

func (a searchArgs) base() []any {
	return []any{a.category, a.brand, a.minPrice, a.maxPrice, a.rating, a.inStock, a.brands, a.categoryIDs}
}

func (h *handlers) Search(ctx context.Context, input *SearchInput) (*struct{ Body SearchResponse }, error) {
	if input.Q == "" && input.Brand == "" {
		return &struct{ Body SearchResponse }{Body: SearchResponse{Products: []catalog.ProductSummary{}}}, nil
	}

	categoryIDs, err := h.resolveDepartmentIDs(ctx, splitAndTrim(input.Categories))
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to resolve departments", err)
	}
	args := searchArgs{
		category: input.Category, brand: input.Brand,
		minPrice: input.MinPrice, maxPrice: input.MaxPrice, rating: input.Rating, inStock: input.InStock,
		brands: splitAndTrim(input.Brands), categoryIDs: categoryIDs, q: input.Q,
	}
	offset := (input.Page - 1) * input.Limit

	var products []catalog.ProductSummary
	var total int
	var facets *catalog.Facets

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		products, err = h.searchProducts(gctx, args, input.Sort, input.Limit, offset)
		return
	})
	g.Go(func() (err error) { total, err = h.countSearch(gctx, args); return })
	if input.Facets {
		facets = &catalog.Facets{}
		g.Go(func() (err error) { facets.Categories, err = h.departmentFacets(gctx, args); return })
		g.Go(func() (err error) { facets.Brands, err = h.brandFacets(gctx, args); return })
		g.Go(func() (err error) { facets.Ratings, err = h.ratingFacet(gctx, args); return })
	}
	if err := g.Wait(); err != nil {
		return nil, huma.Error500InternalServerError("search failed", err)
	}

	return &struct{ Body SearchResponse }{Body: SearchResponse{
		Products: products, Facets: facets, Total: total, Page: input.Page, Limit: input.Limit,
	}}, nil
}

var searchOrderBy = map[string]string{
	"newest":     "p.created_at DESC",
	"price_asc":  "p.price ASC",
	"price_desc": "p.price DESC",
}

func (h *handlers) searchProducts(ctx context.Context, a searchArgs, sort string, limit, offset int64) ([]catalog.ProductSummary, error) {
	columns := `p.id::text, p.name, p.sku, COALESCE(NULLIF(p.brand, ''), b.name) AS brand,
		p.price::float8, p.old_price::float8, p.sale_price::float8,
		p.image_url, p.stock_quantity, p.rating::float8, p.review_count, p.is_featured, p.category_id::text,
		c.name AS category_name`

	var rows pgx.Rows
	var err error
	if a.q == "" {
		// Brand-browse mode: no text query to rank by. `sort` still applies
		// if the caller picked one (e.g. price); "best_match" has no
		// relevance signal here so it falls back to alphabetical, same as
		// before this feature existed.
		orderBy := "p.name ASC"
		if ob, ok := searchOrderBy[sort]; ok {
			orderBy = ob
		}
		rows, err = h.pool.Query(ctx, `SELECT `+columns+` FROM `+searchFrom+` WHERE `+searchWhere+`
			ORDER BY `+orderBy+` LIMIT $9 OFFSET $10`, append(a.base(), limit, offset)...)
	} else {
		orderBy := relevanceExpr + " DESC"
		if sort != "best_match" {
			if ob, ok := searchOrderBy[sort]; ok {
				orderBy = ob
			}
		}
		rows, err = h.pool.Query(ctx, `SELECT `+columns+` FROM `+searchFrom+` WHERE `+searchWhere+`
			  AND `+relevanceExpr+` > $10
			ORDER BY `+orderBy+`
			LIMIT $11 OFFSET $12`, append(a.base(), a.q, relevanceFloor, limit, offset)...)
	}
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[catalog.ProductSummary])
}

func (h *handlers) countSearch(ctx context.Context, a searchArgs) (int, error) {
	var count int
	var err error
	if a.q == "" {
		err = h.pool.QueryRow(ctx, `SELECT count(*) FROM `+searchFrom+` WHERE `+searchWhere, a.base()...).Scan(&count)
	} else {
		err = h.pool.QueryRow(ctx, `SELECT count(*) FROM `+searchFrom+` WHERE `+searchWhere+`
			AND `+relevanceExpr+` > $10`, append(a.base(), a.q, relevanceFloor)...).Scan(&count)
	}
	return count, err
}

// brandFacets self-excludes the brands (plural) filter by passing a copy of
// args with Brands cleared — every other active filter (including Q's
// relevance predicate) still applies.
func (h *handlers) brandFacets(ctx context.Context, a searchArgs) ([]catalog.BrandFacet, error) {
	a.brands = nil
	q := `SELECT COALESCE(NULLIF(p.brand, ''), b.name) AS name, count(*) AS count
		FROM ` + searchFrom + ` WHERE ` + searchWhere + `
		  AND COALESCE(NULLIF(p.brand, ''), b.name) IS NOT NULL`
	args := a.base()
	if a.q != "" {
		q += ` AND ` + relevanceExpr + ` > $10`
		args = append(args, a.q, relevanceFloor)
	}
	q += ` GROUP BY 1 ORDER BY count DESC, 1 ASC`
	rows, err := h.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[catalog.BrandFacet])
}

// ratingFacet self-excludes the rating filter by forcing rating=0 (its own
// "no filter" sentinel) for this query only.
func (h *handlers) ratingFacet(ctx context.Context, a searchArgs) ([]catalog.RatingFacet, error) {
	a.rating = 0
	q := `SELECT
		  count(*) FILTER (WHERE p.rating >= 4) AS r4,
		  count(*) FILTER (WHERE p.rating >= 3) AS r3,
		  count(*) FILTER (WHERE p.rating >= 2) AS r2,
		  count(*) FILTER (WHERE p.rating >= 1) AS r1
		FROM ` + searchFrom + ` WHERE ` + searchWhere
	args := a.base()
	if a.q != "" {
		q += ` AND ` + relevanceExpr + ` > $10`
		args = append(args, a.q, relevanceFloor)
	}
	var r4, r3, r2, r1 int
	if err := h.pool.QueryRow(ctx, q, args...).Scan(&r4, &r3, &r2, &r1); err != nil {
		return nil, err
	}
	return []catalog.RatingFacet{{Rating: 4, Count: r4}, {Rating: 3, Count: r3}, {Rating: 2, Count: r2}, {Rating: 1, Count: r1}}, nil
}

// departmentFacets self-excludes the department (categoryIDs) filter and
// resolves each matching product's top-level ancestor via
// COALESCE(parent_id, id) — the 2-level category-depth assumption (see
// catalog.ProductFilter), no recursive CTE. Own copy of the aggregation
// logic, not a call into catalog.Repo, preserving this package's existing
// deliberate independence from catalog's query layer.
func (h *handlers) departmentFacets(ctx context.Context, a searchArgs) ([]catalog.CategoryFacet, error) {
	a.categoryIDs = nil
	q := `
		SELECT d.id::text, d.name, d.slug, count(matched.product_id) AS count
		FROM categories d
		LEFT JOIN (
		    SELECT COALESCE(top.id, c.id) AS dept_id, p.id AS product_id
		    FROM ` + searchFrom + `
		    LEFT JOIN categories top ON top.id = c.parent_id
		    WHERE ` + searchWhere
	args := a.base()
	if a.q != "" {
		q += ` AND ` + relevanceExpr + ` > $10`
		args = append(args, a.q, relevanceFloor)
	}
	q += `
		) matched ON matched.dept_id = d.id
		WHERE d.parent_id IS NULL
		GROUP BY d.id, d.name, d.slug ORDER BY d.sort_order ASC`
	rows, err := h.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[catalog.CategoryFacet])
}

// resolveDepartmentIDs expands top-level department slugs into their own id
// plus their direct children's ids (2-level assumption, no recursion) — the
// set products.category_id must match for the `categories=` refinement.
func (h *handlers) resolveDepartmentIDs(ctx context.Context, slugs []string) ([]string, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	rows, err := h.pool.Query(ctx, `
		WITH depts AS (SELECT id FROM categories WHERE slug = ANY($1) AND parent_id IS NULL)
		SELECT id::text FROM depts
		UNION SELECT id::text FROM categories WHERE parent_id IN (SELECT id FROM depts)`, slugs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func splitAndTrim(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
