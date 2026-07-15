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
	IsPublished     bool     `db:"is_published" json:"is_published"`
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
