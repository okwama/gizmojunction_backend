package catalog

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Admin-only queries: writes and wider column sets (cost_price, tax_class,
// is_published) never exposed on the public catalog.go endpoints. Every
// caller of these is gated by RequireRole(ADMIN) in admin.go.

// nullIfEmpty lets an empty product/category/brand ID (new record) pass
// through as SQL NULL so `COALESCE($1::uuid, gen_random_uuid())` can assign
// a fresh id — an empty string can't cast directly to uuid.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *Repo) UpsertCategory(ctx context.Context, c Category) (Category, error) {
	rows, err := r.pool.Query(ctx, `
		INSERT INTO categories (id, name, slug, description, parent_id, sort_order, is_visible, image_url, is_featured_on_home)
		VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4, $5::uuid, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			slug = EXCLUDED.slug,
			description = EXCLUDED.description,
			parent_id = EXCLUDED.parent_id,
			sort_order = EXCLUDED.sort_order,
			is_visible = EXCLUDED.is_visible,
			image_url = EXCLUDED.image_url,
			is_featured_on_home = EXCLUDED.is_featured_on_home
		RETURNING `+categoryColumns,
		nullIfEmpty(c.ID), c.Name, c.Slug, c.Description, c.ParentID, c.SortOrder, c.IsVisible, c.ImageURL, c.IsFeaturedOnHome)
	if err != nil {
		return Category{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Category])
}

func (r *Repo) DeleteCategory(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM categories WHERE id = $1`, id)
	return err
}

// MergeCategories reassigns the source category's products and child
// categories to the target, then deletes the source — matching the
// admin UI's existing "merge and delete original" flow.
func (r *Repo) MergeCategories(ctx context.Context, sourceID, targetID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE products SET category_id = $1 WHERE category_id = $2`, targetID, sourceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE categories SET parent_id = $1 WHERE parent_id = $2`, targetID, sourceID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM categories WHERE id = $1`, sourceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repo) UpsertBrand(ctx context.Context, b Brand) (Brand, error) {
	rows, err := r.pool.Query(ctx, `
		INSERT INTO brands (id, name, logo_url, slug, is_featured)
		VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			logo_url = EXCLUDED.logo_url,
			slug = EXCLUDED.slug,
			is_featured = EXCLUDED.is_featured
		RETURNING `+brandColumns,
		nullIfEmpty(b.ID), b.Name, b.ImageURL, b.Slug, b.IsFeatured)
	if err != nil {
		return Brand{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Brand])
}

func (r *Repo) DeleteBrand(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM brands WHERE id = $1`, id)
	return err
}

const adminProductColumns = `p.id::text, p.name, p.sku, p.brand_id::text, b.name AS brand_name, p.category_id::text, c.name AS category_name, p.price::float8, p.sale_price::float8, p.cost_price::float8, p.old_price::float8, p.description_html, p.stock_quantity, p.image_url, p.tax_class, p.is_featured, p.is_published`

const adminProductFrom = `FROM products p LEFT JOIN brands b ON b.id = p.brand_id LEFT JOIN categories c ON c.id = p.category_id`

// ListProductsAdmin supports the admin products table's search box and
// category filter — search matches name or SKU, category is exact match
// (the frontend already resolves "All" to an empty categoryID before
// calling in). Returns the page of results plus the total matching count
// for pagination.
func (r *Repo) ListProductsAdmin(ctx context.Context, search, categoryID string, limit, offset int) ([]AdminProduct, int, error) {
	where := "WHERE 1=1"
	var args []any
	if search != "" {
		args = append(args, "%"+search+"%")
		where += fmt.Sprintf(" AND (p.name ILIKE $%d OR p.sku ILIKE $%d)", len(args), len(args))
	}
	if categoryID != "" {
		args = append(args, categoryID)
		where += fmt.Sprintf(" AND p.category_id = $%d::uuid", len(args))
	}

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) `+adminProductFrom+` `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	limitParam := strconv.Itoa(len(args) - 1)
	offsetParam := strconv.Itoa(len(args))
	rows, err := r.pool.Query(ctx, `
		SELECT `+adminProductColumns+` `+adminProductFrom+` `+where+`
		ORDER BY p.created_at DESC
		LIMIT $`+limitParam+` OFFSET $`+offsetParam, args...)
	if err != nil {
		return nil, 0, err
	}
	products, err := pgx.CollectRows(rows, pgx.RowToStructByName[AdminProduct])
	return products, total, err
}

func (r *Repo) ProductByIDAdmin(ctx context.Context, id string) (AdminProduct, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+adminProductColumns+` `+adminProductFrom+` WHERE p.id = $1`, id)
	if err != nil {
		return AdminProduct{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[AdminProduct])
}

// UpsertProduct deliberately never touches is_published — the admin
// product form has no such field (only the bulk-status action does, see
// BulkUpdateProductStatus), and products.is_published defaults to true at
// the schema level, so new products are published exactly as they were
// under the old direct Supabase upsert.
func (r *Repo) UpsertProduct(ctx context.Context, p AdminProduct) (AdminProduct, error) {
	rows, err := r.pool.Query(ctx, `
		INSERT INTO products (id, name, sku, brand_id, category_id, price, sale_price, cost_price, old_price, description_html, stock_quantity, image_url, tax_class, is_featured, updated_at)
		VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4::uuid, $5::uuid, $6, $7, $8, $9, $10, $11, $12, $13, $14, now())
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			sku = EXCLUDED.sku,
			brand_id = EXCLUDED.brand_id,
			category_id = EXCLUDED.category_id,
			price = EXCLUDED.price,
			sale_price = EXCLUDED.sale_price,
			cost_price = EXCLUDED.cost_price,
			old_price = EXCLUDED.old_price,
			description_html = EXCLUDED.description_html,
			stock_quantity = EXCLUDED.stock_quantity,
			image_url = EXCLUDED.image_url,
			tax_class = EXCLUDED.tax_class,
			is_featured = EXCLUDED.is_featured,
			updated_at = now()
		RETURNING id::text
	`, nullIfEmpty(p.ID), p.Name, p.SKU, p.BrandID, p.CategoryID, p.Price, p.SalePrice, p.CostPrice, p.OldPrice, p.DescriptionHTML, p.StockQty, p.ImageURL, p.TaxClass, p.IsFeatured)
	if err != nil {
		return AdminProduct{}, err
	}
	id, err := pgx.CollectExactlyOneRow(rows, pgx.RowTo[string])
	if err != nil {
		return AdminProduct{}, err
	}
	return r.ProductByIDAdmin(ctx, id)
}

func (r *Repo) DeleteProduct(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	return err
}

func (r *Repo) BulkUpdateProductCategory(ctx context.Context, ids []string, categoryID string) error {
	_, err := r.pool.Exec(ctx, `UPDATE products SET category_id = $1::uuid, updated_at = now() WHERE id = ANY($2::uuid[])`, nullIfEmpty(categoryID), ids)
	return err
}

func (r *Repo) BulkUpdateProductStatus(ctx context.Context, ids []string, isPublished bool) error {
	_, err := r.pool.Exec(ctx, `UPDATE products SET is_published = $1, updated_at = now() WHERE id = ANY($2::uuid[])`, isPublished, ids)
	return err
}

func (r *Repo) BulkDeleteProducts(ctx context.Context, ids []string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM products WHERE id = ANY($1::uuid[])`, ids)
	return err
}

// BulkAdjustPrice recomputes each product's price from its own current
// value in a single UPDATE (price on the right-hand side refers to the
// row's existing value at update time) — never from a client-supplied
// final price, so a stale admin-side preview or a tampered request can't
// set an arbitrary price. Clamped at 0 so a large percentage/fixed
// decrease can't push a price negative.
func (r *Repo) BulkAdjustPrice(ctx context.Context, ids []string, mode string, value float64) error {
	var expr string
	if mode == "percent" {
		expr = `GREATEST(ROUND((price * (1 + $2 / 100.0))::numeric, 2), 0)`
	} else {
		expr = `GREATEST(ROUND((price + $2)::numeric, 2), 0)`
	}
	_, err := r.pool.Exec(ctx, `UPDATE products SET price = `+expr+`, updated_at = now() WHERE id = ANY($1::uuid[])`, ids, value)
	return err
}

func (r *Repo) EmptyProductCatalog(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM products`)
	return err
}

const adminPromotionColumns = `id::text, title, description, banner_url, target_url, is_active, starts_at, ends_at, display_location, badge_text, created_at`

func (r *Repo) ListPromotionsAdmin(ctx context.Context) ([]AdminPromotion, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+adminPromotionColumns+` FROM promotions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[AdminPromotion])
}

func (r *Repo) UpsertPromotion(ctx context.Context, p AdminPromotion) (AdminPromotion, error) {
	rows, err := r.pool.Query(ctx, `
		INSERT INTO promotions (id, title, description, banner_url, target_url, is_active, starts_at, ends_at, display_location, badge_text)
		VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO UPDATE SET
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			banner_url = EXCLUDED.banner_url,
			target_url = EXCLUDED.target_url,
			is_active = EXCLUDED.is_active,
			starts_at = EXCLUDED.starts_at,
			ends_at = EXCLUDED.ends_at,
			display_location = EXCLUDED.display_location,
			badge_text = EXCLUDED.badge_text
		RETURNING `+adminPromotionColumns,
		nullIfEmpty(p.ID), p.Title, p.Description, p.BannerURL, p.TargetURL, p.IsActive, p.StartsAt, p.EndsAt, p.DisplayLocation, p.BadgeText)
	if err != nil {
		return AdminPromotion{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[AdminPromotion])
}

func (r *Repo) DeletePromotion(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM promotions WHERE id = $1`, id)
	return err
}

const adminBlogPostColumns = `id::text, title, slug, excerpt, content, cover_image, is_published, published_at, created_at, updated_at`

func (r *Repo) ListBlogPostsAdmin(ctx context.Context) ([]AdminBlogPost, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+adminBlogPostColumns+` FROM blog_posts ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[AdminBlogPost])
}

// UpsertBlogPost owns the "stamp published_at on first publish" rule the
// admin page previously implemented client-side: publishing a post that
// was never published sets published_at to now; re-saving an already-
// published post keeps its original timestamp; drafts keep NULL.
func (r *Repo) UpsertBlogPost(ctx context.Context, b AdminBlogPost) (AdminBlogPost, error) {
	rows, err := r.pool.Query(ctx, `
		INSERT INTO blog_posts (id, title, slug, excerpt, content, cover_image, is_published, published_at, updated_at)
		VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4, $5, $6, $7, CASE WHEN $7 THEN now() END, now())
		ON CONFLICT (id) DO UPDATE SET
			title = EXCLUDED.title,
			slug = EXCLUDED.slug,
			excerpt = EXCLUDED.excerpt,
			content = EXCLUDED.content,
			cover_image = EXCLUDED.cover_image,
			is_published = EXCLUDED.is_published,
			published_at = CASE
				WHEN EXCLUDED.is_published AND blog_posts.published_at IS NULL THEN now()
				ELSE blog_posts.published_at
			END,
			updated_at = now()
		RETURNING `+adminBlogPostColumns,
		nullIfEmpty(b.ID), b.Title, b.Slug, b.Excerpt, b.Content, b.CoverImage, b.IsPublished)
	if err != nil {
		return AdminBlogPost{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[AdminBlogPost])
}

func (r *Repo) DeleteBlogPost(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM blog_posts WHERE id = $1`, id)
	return err
}
