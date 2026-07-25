package catalog

import (
	"encoding/json"
	"time"
)

// Field sets are deliberately narrower than `SELECT *` on the underlying
// tables: cost_price, tax_class, import_job_id and similar internal/admin
// columns are never selected for these public, unauthenticated endpoints.

type ProductSummary struct {
	ID          string   `db:"id" json:"id"`
	Name        string   `db:"name" json:"name"`
	SKU         string   `db:"sku" json:"sku"`
	Brand       *string  `db:"brand" json:"brand,omitempty"`
	Price       float64  `db:"price" json:"price"`
	OldPrice    *float64 `db:"old_price" json:"old_price,omitempty"`
	SalePrice   *float64 `db:"sale_price" json:"sale_price,omitempty"`
	ImageURL    *string  `db:"image_url" json:"image_url,omitempty"`
	StockQty    int32    `db:"stock_quantity" json:"stock_quantity"`
	Rating      float64  `db:"rating" json:"rating"`
	ReviewCount int32    `db:"review_count" json:"review_count"`
	IsFeatured  bool     `db:"is_featured" json:"is_featured"`
	CategoryID  *string  `db:"category_id" json:"category_id,omitempty"`
	// CategoryName is joined in (not just CategoryID) because the storefront's
	// ProductFilters "Department" checkboxes filter/display by name.
	CategoryName *string `db:"category_name" json:"category_name,omitempty"`
}

// CategoryFacet is one entry in the Department facet returned alongside a
// product listing. Meaning depends on the caller: direct subcategories of
// the current category (category-page drill-down) or top-level departments
// across the whole result set (search page, and the category page's
// slug="all" case).
type CategoryFacet struct {
	ID    string `db:"id" json:"id"`
	Name  string `db:"name" json:"name"`
	Slug  string `db:"slug" json:"slug"`
	Count int    `db:"count" json:"count"`
}

// BrandFacet is one Brand checkbox's live count.
type BrandFacet struct {
	Name  string `db:"name" json:"name"`
	Count int    `db:"count" json:"count"`
}

// RatingFacet is one "N & up" bucket. Count respects every active filter
// except the rating threshold itself (standard faceted-search rule).
type RatingFacet struct {
	Rating int `json:"rating"`
	Count  int `json:"count"`
}

// Facets is the sidebar payload returned alongside a paginated product
// listing. Categories is empty when there's nothing left to drill into
// (e.g. already viewing a subcategory — see the 2-level category depth
// assumption documented on ProductFilter).
type Facets struct {
	Categories []CategoryFacet `json:"categories"`
	Brands     []BrandFacet    `json:"brands"`
	Ratings    []RatingFacet   `json:"ratings"`
}

// Normalize replaces any nil slice with an empty one. A Go nil slice
// encodes to JSON `null`, not `[]` — and every facet field here is a nil
// slice whenever its query legitimately matched zero rows (pgx.CollectRows
// never allocates when there's nothing to collect), or, for Categories
// specifically, whenever the caller skipped populating it at all (e.g. a
// leaf subcategory has nothing further to drill into). The frontend always
// iterates these fields directly (e.g. `facets.categories.length`) without
// a null-guard, so an unnormalized nil here is a real crash, not just an
// empty list — this must be called before every response that includes
// Facets.
func (f *Facets) Normalize() {
	if f.Categories == nil {
		f.Categories = []CategoryFacet{}
	}
	if f.Brands == nil {
		f.Brands = []BrandFacet{}
	}
	if f.Ratings == nil {
		f.Ratings = []RatingFacet{}
	}
}

// ProductFilter bundles the optional filter dimensions shared by every
// product-listing and facet-count query in this package. Zero value means
// "no filter" on that dimension — same convention as the existing
// `$1 = '' OR ...` optional-param pattern already used for category/brand
// matching elsewhere in this backend. A struct (rather than more positional
// args) avoids repeated signature churn as filter dimensions are added.
//
// The category tree is assumed to be exactly 2 levels deep (top-level
// department + direct subcategories) — matches every existing UI surface
// (Header.svelte's topCategories, MegaMenu, ProductFilters' own top-level
// derivation). Facet queries resolve a product's top-level ancestor via a
// single COALESCE(parent_id, id), not a recursive CTE.
type ProductFilter struct {
	MinPrice  float64  // 0 = no lower bound
	MaxPrice  float64  // 0 = no upper bound
	MinRating int      // 0 = none, else 1..4
	InStock   bool
	Brands    []string // canonical (post-COALESCE) brand names; nil/empty = no filter
}

type ProductDetail struct {
	ID               string          `db:"id" json:"id"`
	Name             string          `db:"name" json:"name"`
	SKU              string          `db:"sku" json:"sku"`
	Brand            *string         `db:"brand" json:"brand,omitempty"`
	BrandID          *string         `db:"brand_id" json:"brand_id,omitempty"`
	Description      *string         `db:"description" json:"description,omitempty"`
	DescriptionHTML  *string         `db:"description_html" json:"description_html,omitempty"`
	DescriptionPlain *string         `db:"description_plain" json:"description_plain,omitempty"`
	Price            float64         `db:"price" json:"price"`
	OldPrice         *float64        `db:"old_price" json:"old_price,omitempty"`
	SalePrice        *float64        `db:"sale_price" json:"sale_price,omitempty"`
	StockQty         int32           `db:"stock_quantity" json:"stock_quantity"`
	CategoryID       *string         `db:"category_id" json:"category_id,omitempty"`
	ImageURL         *string         `db:"image_url" json:"image_url,omitempty"`
	Gallery          []string        `db:"gallery" json:"gallery,omitempty"`
	Specifications   json.RawMessage `db:"specifications" json:"specifications,omitempty"`
	WeightKg         *float64        `db:"weight_kg" json:"weight_kg,omitempty"`
	Barcode          *string         `db:"barcode" json:"barcode,omitempty"`
	Rating           float64         `db:"rating" json:"rating"`
	ReviewCount      int32           `db:"review_count" json:"review_count"`
	IsFeatured       bool            `db:"is_featured" json:"is_featured"`
}

type Category struct {
	ID               string  `db:"id" json:"id"`
	Name             string  `db:"name" json:"name"`
	Slug             string  `db:"slug" json:"slug"`
	Description      *string `db:"description" json:"description,omitempty"`
	ParentID         *string `db:"parent_id" json:"parent_id,omitempty"`
	SortOrder        int32   `db:"sort_order" json:"sort_order"`
	IsVisible        bool    `db:"is_visible" json:"is_visible"`
	ImageURL         *string `db:"image_url" json:"image_url,omitempty"`
	IsFeaturedOnHome bool    `db:"is_featured_on_home" json:"is_featured_on_home"`
}

type Brand struct {
	ID         string  `db:"id" json:"id"`
	Name       string  `db:"name" json:"name"`
	ImageURL   *string `db:"image_url" json:"image_url,omitempty"`
	Slug       *string `db:"slug" json:"slug,omitempty"`
	IsFeatured bool    `db:"is_featured" json:"is_featured"`
}

// AdminProduct is the admin-facing product shape: wider than ProductSummary/
// ProductDetail (includes cost_price, tax_class, is_published — never
// exposed on the public catalog endpoints) and flatter than the frontend's
// previous `select('*, brand:brands(*), category:categories(*))` — brand/
// category names are joined in directly rather than nested, since the admin
// list/edit views only ever use the name, not the full related row.
type AdminProduct struct {
	ID              string   `db:"id" json:"id"`
	Name            string   `db:"name" json:"name"`
	SKU             string   `db:"sku" json:"sku"`
	BrandID         *string  `db:"brand_id" json:"brand_id,omitempty"`
	BrandName       *string  `db:"brand_name" json:"brand_name,omitempty"`
	CategoryID      *string  `db:"category_id" json:"category_id,omitempty"`
	CategoryName    *string  `db:"category_name" json:"category_name,omitempty"`
	Price           float64  `db:"price" json:"price"`
	SalePrice       *float64 `db:"sale_price" json:"sale_price,omitempty"`
	CostPrice       *float64 `db:"cost_price" json:"cost_price,omitempty"`
	OldPrice        *float64 `db:"old_price" json:"old_price,omitempty"`
	DescriptionHTML *string  `db:"description_html" json:"description_html,omitempty"`
	StockQty        int32    `db:"stock_quantity" json:"stock_quantity"`
	ImageURL        *string  `db:"image_url" json:"image_url,omitempty"`
	TaxClass        *string  `db:"tax_class" json:"tax_class,omitempty"`
	IsFeatured      bool     `db:"is_featured" json:"is_featured"`
	// required:false — the admin save form never sends it and UpsertProduct
	// deliberately ignores it (publish state changes only via bulk-status).
	IsPublished bool `db:"is_published" json:"is_published" required:"false"`
}

// AdminPromotion is the admin-facing promotion shape: everything the
// public Promotion struct has plus is_active/created_at, which the public
// endpoints deliberately never expose (they filter on is_active instead).
type AdminPromotion struct {
	ID              string     `db:"id" json:"id"`
	Title           string     `db:"title" json:"title"`
	Description     *string    `db:"description" json:"description,omitempty"`
	BannerURL       *string    `db:"banner_url" json:"banner_url,omitempty"`
	TargetURL       *string    `db:"target_url" json:"target_url,omitempty"`
	IsActive        bool       `db:"is_active" json:"is_active"`
	StartsAt        *time.Time `db:"starts_at" json:"starts_at,omitempty"`
	EndsAt          *time.Time `db:"ends_at" json:"ends_at,omitempty"`
	DisplayLocation string     `db:"display_location" json:"display_location"`
	BadgeText       *string    `db:"badge_text" json:"badge_text,omitempty"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
}

// AdminBlogPost is the full blog_posts row for the admin editor — the
// public catalog surface only ever exposes BlogPostSummary.
type AdminBlogPost struct {
	ID          string     `db:"id" json:"id"`
	Title       string     `db:"title" json:"title"`
	Slug        string     `db:"slug" json:"slug"`
	Excerpt     *string    `db:"excerpt" json:"excerpt,omitempty"`
	Content     *string    `db:"content" json:"content,omitempty"`
	CoverImage  *string    `db:"cover_image" json:"cover_image,omitempty"`
	IsPublished bool       `db:"is_published" json:"is_published"`
	PublishedAt *time.Time `db:"published_at" json:"published_at,omitempty"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at" json:"updated_at"`
}

type Promotion struct {
	ID              string     `db:"id" json:"id"`
	Title           string     `db:"title" json:"title"`
	Description     *string    `db:"description" json:"description,omitempty"`
	BannerURL       *string    `db:"banner_url" json:"banner_url,omitempty"`
	TargetURL       *string    `db:"target_url" json:"target_url,omitempty"`
	StartsAt        *time.Time `db:"starts_at" json:"starts_at,omitempty"`
	EndsAt          *time.Time `db:"ends_at" json:"ends_at,omitempty"`
	DisplayLocation string     `db:"display_location" json:"display_location"`
	BadgeText       *string    `db:"badge_text" json:"badge_text,omitempty"`
}

type BlogPostSummary struct {
	Title       string     `db:"title" json:"title"`
	Slug        string     `db:"slug" json:"slug"`
	PublishedAt *time.Time `db:"published_at" json:"published_at,omitempty"`
}

type Review struct {
	ID         string    `db:"id" json:"id"`
	AuthorName *string   `db:"author_name" json:"author_name,omitempty"`
	Rating     int16     `db:"rating" json:"rating"`
	Comment    *string   `db:"comment" json:"comment,omitempty"`
	CreatedAt  time.Time `db:"created_at" json:"created_at"`
}

type SubcategoryWithImage struct {
	Category
	FallbackImage *string `json:"fallback_image,omitempty"`
}

type CategoryWithSubcategories struct {
	Category
	Subcategories []SubcategoryWithImage `json:"subcategories"`
}
