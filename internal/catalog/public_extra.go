package catalog

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
)

// Public endpoints added while migrating the remaining storefront Supabase
// reads (Phase 5): blog pages, the header's featured brands and active
// promotions, and the two XML feed routes (sitemap, Google Merchant feed).

type BlogPostPublic struct {
	Title       string     `db:"title" json:"title"`
	Slug        string     `db:"slug" json:"slug"`
	Excerpt     *string    `db:"excerpt" json:"excerpt,omitempty"`
	Content     *string    `db:"content" json:"content,omitempty"`
	CoverImage  *string    `db:"cover_image" json:"cover_image,omitempty"`
	PublishedAt *time.Time `db:"published_at" json:"published_at,omitempty"`
}

func (r *Repo) PublishedBlogPosts(ctx context.Context) ([]BlogPostPublic, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT title, slug, excerpt, NULL AS content, cover_image, published_at
		FROM blog_posts WHERE is_published = true
		ORDER BY published_at DESC NULLS LAST`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[BlogPostPublic])
}

func (r *Repo) PublishedBlogPostBySlug(ctx context.Context, slug string) (BlogPostPublic, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT title, slug, excerpt, content, cover_image, published_at
		FROM blog_posts WHERE slug = $1 AND is_published = true`, slug)
	if err != nil {
		return BlogPostPublic{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[BlogPostPublic])
}

func (r *Repo) BrandsPublic(ctx context.Context, featuredOnly bool, limit int) ([]Brand, error) {
	query := `SELECT ` + brandColumns + ` FROM brands`
	if featuredOnly {
		query += ` WHERE is_featured = true`
	}
	query += ` ORDER BY name ASC LIMIT $1`
	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Brand])
}

func (r *Repo) ActivePromotionsAll(ctx context.Context) ([]Promotion, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, title, description, banner_url, target_url, starts_at, ends_at, display_location, badge_text
		FROM promotions
		WHERE is_active = true
			AND (starts_at IS NULL OR starts_at <= now())
			AND (ends_at IS NULL OR ends_at >= now())
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Promotion])
}

type SitemapEntry struct {
	Slug      string     `db:"slug" json:"slug"`
	UpdatedAt *time.Time `db:"updated_at" json:"updated_at,omitempty"`
}

type SitemapData struct {
	Products   []SitemapEntry `json:"products"`
	Categories []SitemapEntry `json:"categories"`
	BlogPosts  []SitemapEntry `json:"blog_posts"`
}

func (r *Repo) Sitemap(ctx context.Context) (SitemapData, error) {
	var data SitemapData
	rows, err := r.pool.Query(ctx, `SELECT sku AS slug, updated_at FROM products WHERE is_published = true AND sku IS NOT NULL`)
	if err != nil {
		return data, err
	}
	if data.Products, err = pgx.CollectRows(rows, pgx.RowToStructByName[SitemapEntry]); err != nil {
		return data, err
	}
	rows, err = r.pool.Query(ctx, `SELECT slug, NULL::timestamptz AS updated_at FROM categories WHERE is_visible = true`)
	if err != nil {
		return data, err
	}
	if data.Categories, err = pgx.CollectRows(rows, pgx.RowToStructByName[SitemapEntry]); err != nil {
		return data, err
	}
	rows, err = r.pool.Query(ctx, `SELECT slug, updated_at FROM blog_posts WHERE is_published = true`)
	if err != nil {
		return data, err
	}
	data.BlogPosts, err = pgx.CollectRows(rows, pgx.RowToStructByName[SitemapEntry])
	return data, err
}

type FeedProduct struct {
	ID           string   `db:"id" json:"id"`
	Name         string   `db:"name" json:"name"`
	Description  *string  `db:"description" json:"description,omitempty"`
	SKU          *string  `db:"sku" json:"sku,omitempty"`
	Price        float64  `db:"price" json:"price"`
	SalePrice    *float64 `db:"sale_price" json:"sale_price,omitempty"`
	StockQty     int32    `db:"stock_quantity" json:"stock_quantity"`
	ImageURL     *string  `db:"image_url" json:"image_url,omitempty"`
	CategoryName *string  `db:"category_name" json:"category_name,omitempty"`
	BrandName    *string  `db:"brand_name" json:"brand_name,omitempty"`
}

func (r *Repo) FeedProducts(ctx context.Context) ([]FeedProduct, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id::text, p.name, p.description, p.sku, p.price::float8, p.sale_price::float8,
			p.stock_quantity, p.image_url, c.name AS category_name, b.name AS brand_name
		FROM products p
		LEFT JOIN categories c ON c.id = p.category_id
		LEFT JOIN brands b ON b.id = p.brand_id
		WHERE p.is_published = true`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[FeedProduct])
}

// --- Handlers ---

type emptyPublicInput struct{}

type blogSlugInput struct {
	Slug string `path:"slug"`
}

type brandsInput struct {
	Featured bool `query:"featured"`
	Limit    int  `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

func RegisterExtra(api huma.API, repo *Repo) {
	huma.Register(api, huma.Operation{
		OperationID: "list-blog-posts",
		Method:      http.MethodGet,
		Path:        "/v1/blog",
		Summary:     "Published blog posts",
	}, func(ctx context.Context, _ *emptyPublicInput) (*struct{ Body []BlogPostPublic }, error) {
		posts, err := repo.PublishedBlogPosts(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load blog posts", err)
		}
		return &struct{ Body []BlogPostPublic }{Body: posts}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-blog-post",
		Method:      http.MethodGet,
		Path:        "/v1/blog/{slug}",
		Summary:     "A published blog post by slug",
	}, func(ctx context.Context, input *blogSlugInput) (*struct{ Body BlogPostPublic }, error) {
		post, err := repo.PublishedBlogPostBySlug(ctx, input.Slug)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, huma.Error404NotFound("Article not found")
			}
			return nil, huma.Error500InternalServerError("failed to load blog post", err)
		}
		return &struct{ Body BlogPostPublic }{Body: post}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-brands",
		Method:      http.MethodGet,
		Path:        "/v1/brands",
		Summary:     "Public brand list (optionally featured only)",
	}, func(ctx context.Context, input *brandsInput) (*struct{ Body []Brand }, error) {
		brands, err := repo.BrandsPublic(ctx, input.Featured, input.Limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load brands", err)
		}
		return &struct{ Body []Brand }{Body: brands}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-promotions",
		Method:      http.MethodGet,
		Path:        "/v1/promotions",
		Summary:     "Active promotions",
	}, func(ctx context.Context, _ *emptyPublicInput) (*struct{ Body []Promotion }, error) {
		promos, err := repo.ActivePromotionsAll(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load promotions", err)
		}
		return &struct{ Body []Promotion }{Body: promos}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-sitemap-data",
		Method:      http.MethodGet,
		Path:        "/v1/sitemap",
		Summary:     "Slugs and timestamps for sitemap generation",
	}, func(ctx context.Context, _ *emptyPublicInput) (*struct{ Body SitemapData }, error) {
		data, err := repo.Sitemap(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load sitemap data", err)
		}
		return &struct{ Body SitemapData }{Body: data}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-product-feed",
		Method:      http.MethodGet,
		Path:        "/v1/feed/products",
		Summary:     "Published products with category/brand names for the Google Merchant feed",
	}, func(ctx context.Context, _ *emptyPublicInput) (*struct{ Body []FeedProduct }, error) {
		products, err := repo.FeedProducts(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load product feed", err)
		}
		return &struct{ Body []FeedProduct }{Body: products}, nil
	})
}
