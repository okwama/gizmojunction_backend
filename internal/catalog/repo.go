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

const productSummaryColumns = `id::text, name, sku, brand, price::float8, old_price::float8, sale_price::float8, image_url, stock_quantity, rating::float8, review_count, is_featured, category_id::text`

func (r *Repo) FeaturedProducts(ctx context.Context, limit int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM products WHERE is_published = true ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

// RecentProductsAnyStatus is the fallback used only when FeaturedProducts
// comes back empty (e.g. a fresh catalog with nothing published yet).
func (r *Repo) RecentProductsAnyStatus(ctx context.Context, limit int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM products ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

func (r *Repo) NewArrivals(ctx context.Context, limit int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM products WHERE is_published = true ORDER BY created_at DESC LIMIT $1`, limit)
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

func (r *Repo) ProductsByCategoryIDs(ctx context.Context, categoryIDs []string, limit, offset int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM products WHERE is_published = true AND category_id = ANY($1::uuid[]) ORDER BY created_at DESC LIMIT $2 OFFSET $3`, categoryIDs, limit, offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

func (r *Repo) CountProductsByCategoryIDs(ctx context.Context, categoryIDs []string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM products WHERE is_published = true AND category_id = ANY($1::uuid[])`, categoryIDs).Scan(&count)
	return count, err
}

func (r *Repo) ProductsAll(ctx context.Context, limit, offset int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM products WHERE is_published = true ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

func (r *Repo) CountProductsAll(ctx context.Context) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM products WHERE is_published = true`).Scan(&count)
	return count, err
}

func (r *Repo) DistinctBrandsByCategoryIDs(ctx context.Context, categoryIDs []string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT brand FROM products WHERE is_published = true AND category_id = ANY($1::uuid[]) AND brand IS NOT NULL AND brand != ''`, categoryIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (r *Repo) DistinctBrandsAll(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT brand FROM products WHERE is_published = true AND brand IS NOT NULL AND brand != ''`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
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
const brandColumns = `id::text, name, logo_url AS image_url, slug`

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
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM products WHERE is_published = true AND category_id = ANY($1::uuid[]) AND id != $2 ORDER BY created_at DESC LIMIT $3`, categoryIDs, excludeID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}

func (r *Repo) UpsellProducts(ctx context.Context, brand, excludeID string, limit int) ([]ProductSummary, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM products WHERE is_published = true AND brand = $1 AND id != $2 ORDER BY rating DESC LIMIT $3`, brand, excludeID, limit)
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
	rows, err := r.pool.Query(ctx, `SELECT `+productSummaryColumns+` FROM products WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProductSummary])
}
