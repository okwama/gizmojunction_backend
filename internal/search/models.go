package search

// ProductDoc is the Meilisearch document shape for a product — flat, and
// deliberately close to catalog.ProductSummary's public JSON shape so the
// frontend can feed a search result straight into the same <ProductCard>
// used everywhere else (homepage, category listings, wishlist).
type ProductDoc struct {
	ID           string  `db:"id" json:"id"`
	Name         string  `db:"name" json:"name"`
	SKU          string  `db:"sku" json:"sku"`
	Brand        string  `db:"brand" json:"brand"`
	CategoryID   string  `db:"category_id" json:"category_id,omitempty"`
	CategoryName string  `db:"category_name" json:"category_name,omitempty"`
	CategorySlug string  `db:"category_slug" json:"category_slug,omitempty"`
	Price        float64 `db:"price" json:"price"`
	OldPrice     float64 `db:"old_price" json:"old_price,omitempty"`
	SalePrice    float64 `db:"sale_price" json:"sale_price,omitempty"`
	ImageURL     string  `db:"image_url" json:"image_url,omitempty"`
	StockQty     int32   `db:"stock_quantity" json:"stock_quantity"`
	Rating       float64 `db:"rating" json:"rating"`
	ReviewCount  int32   `db:"review_count" json:"review_count"`
	IsFeatured   bool    `db:"is_featured" json:"is_featured"`

	// IsPublished must round-trip through Meilisearch (it's a filterable
	// attribute — see Client.Search) even though the frontend never uses
	// it, since a search hit is always published by construction.
	IsPublished bool `db:"is_published" json:"is_published"`
}
