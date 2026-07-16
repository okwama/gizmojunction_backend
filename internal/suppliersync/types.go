// Package suppliersync is the fold-in of the standalone /api Vercel service
// (upload/apply/publish/link/categories/settings/override) into this
// backend, per the migration plan's "fold into backend/ later
// opportunistically" note. The parser/diff/resolver logic is ported
// verbatim from api/_utils; only the data access changed (supabase-go REST
// → pgx SQL against Neon) plus admin JWT auth, which the old endpoints
// never had. One type fix along the way: store_category_id is a uuid
// (categories.id) — the old *int typing could never round-trip it.
package suppliersync

type SupplierProduct struct {
	PartNo           string `json:"part_no" db:"part_no"`
	Brand            string `json:"brand" db:"brand"`
	SheetName        string `json:"sheet_name" db:"sheet_name"`
	SupplierCategory string `json:"supplier_category" db:"supplier_category"`
	Description      string `json:"description" db:"description"`
	Availability     string `json:"availability" db:"availability"`
	SupplierPrice    int    `json:"supplier_price" db:"supplier_price"`
	SourceVersion    string `json:"source_version" db:"source_version"`
	IsStoreListed    bool   `json:"is_store_listed" db:"-"`
	StoreSKU         string `json:"store_sku,omitempty" db:"-"`
}

type CategoryMapping struct {
	SupplierSheet     string  `json:"supplier_sheet" db:"supplier_sheet"`
	SupplierCategory  string  `json:"supplier_category" db:"supplier_category"`
	StoreCategoryID   *string `json:"store_category_id" db:"store_category_id"`
	StoreCategoryName *string `json:"store_category_name" db:"store_category_name"`
	IsIgnored         bool    `json:"is_ignored" db:"is_ignored"`
}

type StoreProduct struct {
	PartNo      string `json:"part_no" db:"part_no"`
	StoreSKU    string `json:"store_sku" db:"store_sku"`
	StoreName   string `json:"store_name" db:"store_name"`
	StorePrice  int    `json:"store_price" db:"store_price"`
	IsListed    bool   `json:"is_listed" db:"is_listed"`
	NeedsReview bool   `json:"needs_review" db:"needs_review"`
}

type EnrichedProduct struct {
	SupplierProduct
	StoreCategoryID   *string `json:"store_category_id"`
	StoreCategoryName string  `json:"store_category_name"`
	CategoryState     string  `json:"category_state"` // "mapped", "ignored", "unmapped"
}

type UnmappedCategory struct {
	SupplierSheet    string            `json:"supplier_sheet"`
	SupplierCategory string            `json:"supplier_category"`
	ProductCount     int               `json:"product_count"`
	SampleProducts   []SupplierProduct `json:"sample_products"`
}

type CategoryResolutionResult struct {
	EnrichedProducts    []EnrichedProduct  `json:"enriched_products"`
	UnmappedCategories  []UnmappedCategory `json:"unmapped_categories"`
	HasBlockingUnmapped bool               `json:"has_blocking_unmapped"`
}

type PriceChange struct {
	PartNo            string `json:"part_no"`
	Brand             string `json:"brand"`
	SupplierCategory  string `json:"supplier_category"`
	StoreCategoryName string `json:"store_category_name"`
	Description       string `json:"description"`
	OldPrice          int    `json:"old_price"`
	NewPrice          int    `json:"new_price"`
	PriceDelta        int    `json:"price_delta"`
	PctChange         int    `json:"pct_change"`
	Availability      string `json:"availability"`
	IsStoreListed     bool   `json:"is_store_listed"`
	StoreSKU          string `json:"store_sku,omitempty"`
	StorePrice        int    `json:"store_price,omitempty"`
}

type AvailabilityChange struct {
	PartNo          string `json:"part_no"`
	Brand           string `json:"brand"`
	Description     string `json:"description"`
	OldAvailability string `json:"old_availability"`
	NewAvailability string `json:"new_availability"`
	SupplierPrice   int    `json:"supplier_price"`
	IsStoreListed   bool   `json:"is_store_listed"`
	StoreSKU        string `json:"store_sku,omitempty"`
}

type DiffSummary struct {
	TotalIncoming       int `json:"total_incoming"`
	NewProducts         int `json:"new_products"`
	PriceChanges        int `json:"price_changes"`
	AvailabilityChanges int `json:"availability_changes"`
	Discontinued        int `json:"discontinued"`
	Unchanged           int `json:"unchanged"`
	Ignored             int `json:"ignored"`
}

type DiffReport struct {
	SourceVersion       string               `json:"source_version"`
	GeneratedAt         string               `json:"generated_at"`
	HasBlockingUnmapped bool                 `json:"has_blocking_unmapped"`
	UnmappedCategories  []UnmappedCategory   `json:"unmapped_categories"`
	Summary             DiffSummary          `json:"summary"`
	NewProducts         []EnrichedProduct    `json:"new_products"`
	PriceChanges        []PriceChange        `json:"price_changes"`
	AvailabilityChanges []AvailabilityChange `json:"availability_changes"`
	Discontinued        []SupplierProduct    `json:"discontinued"`
	UnchangedCount      int                  `json:"unchanged_count"`
}
