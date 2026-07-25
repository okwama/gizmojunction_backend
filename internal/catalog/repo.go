package catalog

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo holds every query for the public catalog endpoints. Every query is a
// plain, visible SQL string — no ORM, no lazy-loading — selecting only the
// columns a public unauthenticated response should expose (cost_price,
// tax_class and similar admin-only columns are never selected here).
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// category_name is joined in here (rather than left for the frontend to
// resolve from category_id) because ProductFilters.svelte's Department
// facet displays/filters by category *name* on both the category listing
// and search pages — without it every product's category name was
// undefined, so checking any department silently emptied the results.
//
// brand resolves through the brands join (matching internal/search's own
// query) rather than the raw products.brand column, because that column is
// unreliable — imports populate brand_id, not the denormalized text. Every
// function sharing these two constants picks up the more-reliable brand for
// free, which also keeps brand facet counts and the brands= filter
// consistent with what's actually displayed.
const productSummaryColumns = `p.id::text, p.name, p.sku, COALESCE(NULLIF(p.brand, ''), b.name) AS brand, p.price::float8, p.old_price::float8, p.sale_price::float8, p.image_url, p.stock_quantity, p.rating::float8, p.review_count, p.is_featured, p.category_id::text, c.name AS category_name`
const productSummaryFrom = `products p LEFT JOIN categories c ON c.id = p.category_id LEFT JOIN brands b ON b.id = p.brand_id`

// categoryOrderBy whitelists the only ORDER BY fragments that may be
// string-concatenated into a query — never derived from raw user input.
// Huma's `enum:` tag on Sort rejects unlisted values at the input-binding
// layer; this map additionally defaults any unrecognized value defensively.
var categoryOrderBy = map[string]string{
	"newest":     "p.created_at DESC",
	"price_asc":  "p.price ASC",
	"price_desc": "p.price DESC",
	"best_match": "p.created_at DESC", // no relevance signal exists outside search
}

func resolveOrderBy(sort string) string {
	if orderBy, ok := categoryOrderBy[sort]; ok {
		return orderBy
	}
	return categoryOrderBy["newest"]
}

func (r *Repo) FeaturedProducts(ctx context.Context, limit int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM `+productSummaryFrom+` WHERE p.is_published = true ORDER BY p.updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

// RecentProductsAnyStatus is the fallback used only when FeaturedProducts
// comes back empty (e.g. a fresh catalog with nothing published yet).
func (r *Repo) RecentProductsAnyStatus(ctx context.Context, limit int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM `+productSummaryFrom+` ORDER BY p.updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

func (r *Repo) NewArrivals(ctx context.Context, limit int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM `+productSummaryFrom+` WHERE p.is_published = true ORDER BY p.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

const categoryColumns = `id::text, name, slug, description, parent_id::text, sort_order, is_visible, image_url, is_featured_on_home`

func (r *Repo) FeaturedCategories(ctx context.Context) ([]Category, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+categoryColumns+` FROM categories WHERE is_featured_on_home = true ORDER BY sort_order ASC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Category])
}

func (r *Repo) SubcategoriesByParentIDs(ctx context.Context, parentIDs []string) ([]Category, error) {
	if len(parentIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+categoryColumns+` FROM categories WHERE parent_id = ANY($1::uuid[]) ORDER BY sort_order ASC`, parentIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Category])
}

func (r *Repo) ListCategories(ctx context.Context) ([]Category, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+categoryColumns+` FROM categories ORDER BY sort_order ASC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Category])
}

func (r *Repo) CategoryBySlug(ctx context.Context, slug string) (Category, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+categoryColumns+` FROM categories WHERE slug = $1`, slug)
	if err != nil {
		return Category{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Category])
}

func (r *Repo) CategoryByID(ctx context.Context, id string) (Category, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+categoryColumns+` FROM categories WHERE id = $1`, id)
	if err != nil {
		return Category{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Category])
}

// CategoryDescendantIDs walks the category tree in a single recursive query
// instead of the frontend's previous approach of fetching every category
// and recursing over it in JavaScript.
func (r *Repo) CategoryDescendantIDs(ctx context.Context, rootID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		WITH RECURSIVE descendants AS (
			SELECT id FROM categories WHERE id = $1
			UNION ALL
			SELECT c.id FROM categories c JOIN descendants d ON c.parent_id = d.id
		)
		SELECT id::text FROM descendants
	`, rootID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

type categoryImage struct {
	CategoryID string  `db:"category_id"`
	ImageURL   *string `db:"image_url"`
}

// RepresentativeImagesByCategoryIDs returns one representative product image
// per category id in a single query. Called first with subcategory ids,
// then (for misses) with parent category ids, then AnyProductImage as a
// last resort — three set-based queries total, replacing the original
// per-subcategory nested-loop fallback.
func (r *Repo) RepresentativeImagesByCategoryIDs(ctx context.Context, categoryIDs []string) (map[string]string, error) {
	if len(categoryIDs) == 0 {
		return map[string]string{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (category_id) category_id::text, image_url
		FROM products
		WHERE category_id = ANY($1::uuid[]) AND image_url IS NOT NULL
		ORDER BY category_id, updated_at DESC
	`, categoryIDs)
	if err != nil {
		return nil, err
	}
	results, err := pgx.CollectRows(rows, pgx.RowToStructByName[categoryImage])
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(results))
	for _, res := range results {
		if res.ImageURL != nil {
			out[res.CategoryID] = *res.ImageURL
		}
	}
	return out, nil
}

func (r *Repo) AnyProductImage(ctx context.Context) (string, error) {
	var img *string
	err := r.pool.QueryRow(ctx, `SELECT image_url FROM products WHERE image_url IS NOT NULL ORDER BY updated_at DESC LIMIT 1`).Scan(&img)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if img == nil {
		return "", nil
	}
	return *img, nil
}

const promotionColumns = `id::text, title, description, banner_url, target_url, starts_at, ends_at, display_location, badge_text`

func (r *Repo) ActivePromotionsByLocation(ctx context.Context, location string, limit int) ([]Promotion, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+promotionColumns+` FROM promotions WHERE is_active = true AND display_location = $1 ORDER BY created_at DESC LIMIT $2`, location, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Promotion])
}

func (r *Repo) RecentBlogPosts(ctx context.Context, limit int) ([]BlogPostSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT title, slug, published_at FROM blog_posts WHERE is_published = true ORDER BY published_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[BlogPostSummary])
}

// filterArgs is the fixed positional-arg tail every product-listing/facet
// query below appends, in this order: min_price, max_price, min_rating,
// in_stock, brands. Each is a sentinel-based optional filter (0/false/empty
// = no filter) rather than a pointer type — there's no existing precedent in
// this backend for pointer-typed Huma query params, and these sentinels are
// all semantically safe (no non-negative price is ever legitimately 0 as a
// bound, no rating is legitimately below 1).
func filterArgs(f ProductFilter) []any {
	return []any{f.MinPrice, f.MaxPrice, f.MinRating, f.InStock, f.Brands}
}

func (r *Repo) ProductsByCategoryIDs(ctx context.Context, categoryIDs []string, filter ProductFilter, sort string, limit, offset int) ([]ProductSummary, error) {
	args := append([]any{categoryIDs}, filterArgs(filter)...)
	args = append(args, limit, offset)
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM `+productSummaryFrom+`
		WHERE p.is_published = true AND p.category_id = ANY($1::uuid[])
		  AND ($2 <= 0 OR p.price >= $2) AND ($3 <= 0 OR p.price <= $3)
		  AND ($4 = 0 OR p.rating >= $4) AND ($5 = false OR p.stock_quantity > 0)
		  AND (array_length($6::text[], 1) IS NULL OR COALESCE(NULLIF(p.brand,''), b.name) = ANY($6))
		ORDER BY `+resolveOrderBy(sort)+` LIMIT $7 OFFSET $8`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

func (r *Repo) CountProductsByCategoryIDs(ctx context.Context, categoryIDs []string, filter ProductFilter) (int, error) {
	args := append([]any{categoryIDs}, filterArgs(filter)...)
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM products p LEFT JOIN brands b ON b.id = p.brand_id
		WHERE p.is_published = true AND p.category_id = ANY($1::uuid[])
		  AND ($2 <= 0 OR p.price >= $2) AND ($3 <= 0 OR p.price <= $3)
		  AND ($4 = 0 OR p.rating >= $4) AND ($5 = false OR p.stock_quantity > 0)
		  AND (array_length($6::text[], 1) IS NULL OR COALESCE(NULLIF(p.brand,''), b.name) = ANY($6))`, args...).Scan(&count)
	return count, err
}

func (r *Repo) ProductsAll(ctx context.Context, filter ProductFilter, sort string, limit, offset int) ([]ProductSummary, error) {
	args := append(filterArgs(filter), limit, offset)
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM `+productSummaryFrom+`
		WHERE p.is_published = true
		  AND ($1 <= 0 OR p.price >= $1) AND ($2 <= 0 OR p.price <= $2)
		  AND ($3 = 0 OR p.rating >= $3) AND ($4 = false OR p.stock_quantity > 0)
		  AND (array_length($5::text[], 1) IS NULL OR COALESCE(NULLIF(p.brand,''), b.name) = ANY($5))
		ORDER BY `+resolveOrderBy(sort)+` LIMIT $6 OFFSET $7`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

func (r *Repo) CountProductsAll(ctx context.Context, filter ProductFilter) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM products p LEFT JOIN brands b ON b.id = p.brand_id
		WHERE p.is_published = true
		  AND ($1 <= 0 OR p.price >= $1) AND ($2 <= 0 OR p.price <= $2)
		  AND ($3 = 0 OR p.rating >= $3) AND ($4 = false OR p.stock_quantity > 0)
		  AND (array_length($5::text[], 1) IS NULL OR COALESCE(NULLIF(p.brand,''), b.name) = ANY($5))`, filterArgs(filter)...).Scan(&count)
	return count, err
}

// SubcategoryFacets returns the direct subcategories of parentID with a live
// product count each, applying every active filter except department itself
// (department here is drill-down navigation, not a filter dimension — see
// the 2-level assumption on ProductFilter). Predicates on p.* live in the
// JOIN condition, not WHERE, so a subcategory with zero matches still
// appears with count = 0 instead of vanishing.
func (r *Repo) SubcategoryFacets(ctx context.Context, parentID string, filter ProductFilter) ([]CategoryFacet, error) {
	args := append([]any{parentID}, filterArgs(filter)...)
	rows, err := r.pool.Query(ctx, `
		SELECT c.id::text, c.name, c.slug, count(p.id) AS count
		FROM categories c
		LEFT JOIN products p
		    ON p.category_id = c.id AND p.is_published = true
		   AND ($2 <= 0 OR p.price >= $2) AND ($3 <= 0 OR p.price <= $3)
		   AND ($4 = 0 OR p.rating >= $4) AND ($5 = false OR p.stock_quantity > 0)
		LEFT JOIN brands b ON b.id = p.brand_id
		WHERE c.parent_id = $1
		  AND (array_length($6::text[], 1) IS NULL OR COALESCE(NULLIF(p.brand,''), b.name) = ANY($6) OR p.id IS NULL)
		GROUP BY c.id, c.name, c.slug ORDER BY c.sort_order ASC`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CategoryFacet])
}

// DepartmentFacets returns every top-level department with a live product
// count each, across the whole catalog. Used by the search page (which has
// no single category scope) and the category page's slug="all" case. Each
// product's top-level ancestor is resolved once via COALESCE(parent_id,
// id) — the entire benefit of the 2-level assumption, no recursive CTE.
func (r *Repo) DepartmentFacets(ctx context.Context, filter ProductFilter) ([]CategoryFacet, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT d.id::text, d.name, d.slug, count(matched.product_id) AS count
		FROM categories d
		LEFT JOIN (
		    SELECT COALESCE(top.id, c.id) AS dept_id, p.id AS product_id
		    FROM products p
		    JOIN categories c ON c.id = p.category_id
		    LEFT JOIN categories top ON top.id = c.parent_id
		    LEFT JOIN brands b ON b.id = p.brand_id
		    WHERE p.is_published = true
		      AND ($1 <= 0 OR p.price >= $1) AND ($2 <= 0 OR p.price <= $2)
		      AND ($3 = 0 OR p.rating >= $3) AND ($4 = false OR p.stock_quantity > 0)
		      AND (array_length($5::text[], 1) IS NULL OR COALESCE(NULLIF(p.brand,''), b.name) = ANY($5))
		) matched ON matched.dept_id = d.id
		WHERE d.parent_id IS NULL
		GROUP BY d.id, d.name, d.slug ORDER BY d.sort_order ASC`, filterArgs(filter)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CategoryFacet])
}

// BrandFacets returns a live count per brand, self-excluding the brand
// filter (every other active filter still applies). categoryIDs == nil
// scopes to the whole catalog.
func (r *Repo) BrandFacets(ctx context.Context, categoryIDs []string, filter ProductFilter) ([]BrandFacet, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT COALESCE(NULLIF(p.brand, ''), b.name) AS name, count(*) AS count
		FROM products p LEFT JOIN brands b ON b.id = p.brand_id
		WHERE p.is_published = true
		  AND (array_length($1::uuid[], 1) IS NULL OR p.category_id = ANY($1))
		  AND ($2 <= 0 OR p.price >= $2) AND ($3 <= 0 OR p.price <= $3)
		  AND ($4 = 0 OR p.rating >= $4) AND ($5 = false OR p.stock_quantity > 0)
		  AND COALESCE(NULLIF(p.brand, ''), b.name) IS NOT NULL
		GROUP BY 1 ORDER BY count DESC, 1 ASC`,
		categoryIDs, filter.MinPrice, filter.MaxPrice, filter.MinRating, filter.InStock)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[BrandFacet])
}

// RatingFacet returns the four "N & up" bucket counts, self-excluding the
// rating filter. categoryIDs == nil scopes to the whole catalog.
func (r *Repo) RatingFacet(ctx context.Context, categoryIDs []string, filter ProductFilter) ([]RatingFacet, error) {
	var r4, r3, r2, r1 int
	err := r.pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE p.rating >= 4) AS r4,
		  count(*) FILTER (WHERE p.rating >= 3) AS r3,
		  count(*) FILTER (WHERE p.rating >= 2) AS r2,
		  count(*) FILTER (WHERE p.rating >= 1) AS r1
		FROM products p LEFT JOIN brands b ON b.id = p.brand_id
		WHERE p.is_published = true
		  AND (array_length($1::uuid[], 1) IS NULL OR p.category_id = ANY($1))
		  AND ($2 <= 0 OR p.price >= $2) AND ($3 <= 0 OR p.price <= $3)
		  AND ($4 = false OR p.stock_quantity > 0)
		  AND (array_length($5::text[], 1) IS NULL OR COALESCE(NULLIF(p.brand,''), b.name) = ANY($5))`,
		categoryIDs, filter.MinPrice, filter.MaxPrice, filter.InStock, filter.Brands).Scan(&r4, &r3, &r2, &r1)
	if err != nil {
		return nil, err
	}
	return []RatingFacet{{Rating: 4, Count: r4}, {Rating: 3, Count: r3}, {Rating: 2, Count: r2}, {Rating: 1, Count: r1}}, nil
}

const productDetailColumns = `id::text, name, sku, brand, brand_id::text, description, description_html, description_plain, price::float8, old_price::float8, sale_price::float8, stock_quantity, category_id::text, image_url, gallery, specifications, weight_kg::float8, barcode, rating::float8, review_count, is_featured`

func (r *Repo) ProductBySKU(ctx context.Context, sku string) (ProductDetail, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productDetailColumns+` FROM products WHERE sku = $1`, sku)
	if err != nil {
		return ProductDetail{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[ProductDetail])
}

// brands.logo_url is aliased to image_url here — the frontend has always
// expected `image_url` on a brand; this is a column-name mapping, not a
// schema change.
const brandColumns = `id::text, name, logo_url AS image_url, slug, is_featured`

func (r *Repo) BrandByID(ctx context.Context, id string) (Brand, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+brandColumns+` FROM brands WHERE id = $1`, id)
	if err != nil {
		return Brand{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Brand])
}

func (r *Repo) SiblingCategories(ctx context.Context, parentID string) ([]Category, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+categoryColumns+` FROM categories WHERE parent_id = $1 ORDER BY sort_order ASC`, parentID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Category])
}

func (r *Repo) RelatedProducts(ctx context.Context, categoryIDs []string, excludeID string, limit int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM `+productSummaryFrom+` WHERE p.is_published = true AND p.category_id = ANY($1::uuid[]) AND p.id != $2 ORDER BY p.created_at DESC LIMIT $3`, categoryIDs, excludeID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

func (r *Repo) UpsellProducts(ctx context.Context, brand, excludeID string, limit int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM `+productSummaryFrom+` WHERE p.is_published = true AND p.brand = $1 AND p.id != $2 ORDER BY p.rating DESC LIMIT $3`, brand, excludeID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

func (r *Repo) ReviewsByProductID(ctx context.Context, productID string) ([]Review, error) {
	rows, err := r.pool.Query(ctx, `SELECT id::text, author_name, rating, comment, created_at FROM reviews WHERE product_id = $1 ORDER BY created_at DESC`, productID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Review])
}

func (r *Repo) ProductsByIDs(ctx context.Context, ids []string) ([]ProductSummary, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM `+productSummaryFrom+` WHERE p.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}
