package suppliersync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// --- Sync sessions ---

type SyncSession struct {
	ID            string          `db:"id" json:"id"`
	SourceVersion *string         `db:"source_version" json:"source_version"`
	Status        string          `db:"status" json:"status"`
	DiffReport    json.RawMessage `db:"diff_report" json:"diff_report"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
	AppliedAt     *time.Time      `db:"applied_at" json:"applied_at,omitempty"`
}

const sessionColumns = `id::text, source_version, status, diff_report, created_at, applied_at`

func (r *Repo) PendingSession(ctx context.Context) (*SyncSession, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sessionColumns+` FROM sync_sessions WHERE status = 'pending' LIMIT 1`)
	if err != nil {
		return nil, err
	}
	s, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[SyncSession])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repo) DiscardPendingSessions(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM sync_sessions WHERE status = 'pending'`)
	return err
}

func (r *Repo) SessionByID(ctx context.Context, id string) (*SyncSession, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sessionColumns+` FROM sync_sessions WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	s, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[SyncSession])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repo) InsertSession(ctx context.Context, sourceVersion string, report DiffReport) (string, error) {
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	var id string
	err = r.pool.QueryRow(ctx, `
		INSERT INTO sync_sessions (source_version, status, diff_report)
		VALUES ($1, 'pending', $2)
		RETURNING id::text`, sourceVersion, reportJSON).Scan(&id)
	return id, err
}

func (r *Repo) MarkSessionApplied(ctx context.Context, id string) (time.Time, error) {
	appliedAt := time.Now().UTC()
	_, err := r.pool.Exec(ctx, `UPDATE sync_sessions SET status = 'applied', applied_at = $2 WHERE id = $1`, id, appliedAt)
	return appliedAt, err
}

func (r *Repo) ListSessions(ctx context.Context) ([]SyncSession, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sessionColumns+` FROM sync_sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[SyncSession])
}

// --- Category mappings ---

type MappingRow struct {
	ID                int32   `db:"id" json:"id"`
	SupplierSheet     string  `db:"supplier_sheet" json:"supplier_sheet"`
	SupplierCategory  string  `db:"supplier_category" json:"supplier_category"`
	StoreCategoryID   *string `db:"store_category_id" json:"store_category_id"`
	StoreCategoryName *string `db:"store_category_name" json:"store_category_name"`
	IsIgnored         bool    `db:"is_ignored" json:"is_ignored"`
}

const mappingColumns = `id, supplier_sheet, supplier_category, store_category_id::text, store_category_name, is_ignored`

func (r *Repo) Mappings(ctx context.Context) ([]MappingRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+mappingColumns+` FROM supplier_category_mappings ORDER BY supplier_sheet, supplier_category`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[MappingRow])
}

func (r *Repo) mappingsAsDomain(ctx context.Context) ([]CategoryMapping, error) {
	rows, err := r.Mappings(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CategoryMapping, 0, len(rows))
	for _, m := range rows {
		out = append(out, CategoryMapping{
			SupplierSheet:     m.SupplierSheet,
			SupplierCategory:  m.SupplierCategory,
			StoreCategoryID:   m.StoreCategoryID,
			StoreCategoryName: m.StoreCategoryName,
			IsIgnored:         m.IsIgnored,
		})
	}
	return out, nil
}

// UpsertMappingAction mirrors the old PostgREST partial-upsert semantics:
// each action only touches its own columns on conflict.
func (r *Repo) UpsertMappingAction(ctx context.Context, action, sheet, category string, storeCategoryID, storeCategoryName *string) error {
	switch action {
	case "map":
		_, err := r.pool.Exec(ctx, `
			INSERT INTO supplier_category_mappings (supplier_sheet, supplier_category, store_category_id, store_category_name, is_ignored, mapped_at)
			VALUES ($1, $2, $3::uuid, $4, false, now())
			ON CONFLICT (supplier_sheet, supplier_category) DO UPDATE SET
				store_category_id = EXCLUDED.store_category_id,
				store_category_name = EXCLUDED.store_category_name,
				is_ignored = false,
				mapped_at = now()`, sheet, category, storeCategoryID, storeCategoryName)
		return err
	case "ignore":
		_, err := r.pool.Exec(ctx, `
			INSERT INTO supplier_category_mappings (supplier_sheet, supplier_category, is_ignored)
			VALUES ($1, $2, true)
			ON CONFLICT (supplier_sheet, supplier_category) DO UPDATE SET
				is_ignored = true,
				store_category_id = NULL,
				store_category_name = NULL`, sheet, category)
		return err
	case "unignore":
		_, err := r.pool.Exec(ctx, `
			INSERT INTO supplier_category_mappings (supplier_sheet, supplier_category, is_ignored)
			VALUES ($1, $2, false)
			ON CONFLICT (supplier_sheet, supplier_category) DO UPDATE SET
				is_ignored = false`, sheet, category)
		return err
	default:
		return fmt.Errorf("invalid mapping action %q", action)
	}
}

func (r *Repo) SeedUnmappedMappings(ctx context.Context, cats []UnmappedCategory) error {
	for _, cat := range cats {
		if _, err := r.pool.Exec(ctx, `
			INSERT INTO supplier_category_mappings (supplier_sheet, supplier_category, is_ignored)
			VALUES ($1, $2, false)
			ON CONFLICT (supplier_sheet, supplier_category) DO NOTHING`,
			cat.SupplierSheet, cat.SupplierCategory); err != nil {
			return err
		}
	}
	return nil
}

// --- Sync settings ---

type SyncSetting struct {
	Key   string          `db:"key" json:"key"`
	Value json.RawMessage `db:"value" json:"value"`
}

func (r *Repo) SyncSettings(ctx context.Context) ([]SyncSetting, error) {
	rows, err := r.pool.Query(ctx, `SELECT key, value FROM sync_settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[SyncSetting])
}

func (r *Repo) UpsertSyncSetting(ctx context.Context, key string, value json.RawMessage) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO sync_settings (key, value, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}

// DefaultMarkup returns the configured markup percentage, defaulting to 60
// exactly like the old /api service did.
func (r *Repo) DefaultMarkup(ctx context.Context) float64 {
	var raw json.RawMessage
	if err := r.pool.QueryRow(ctx, `SELECT value FROM sync_settings WHERE key = 'default_markup'`).Scan(&raw); err != nil {
		return 60.0
	}
	var asNumber float64
	if json.Unmarshal(raw, &asNumber) == nil {
		return asNumber
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		if f, err := strconv.ParseFloat(asString, 64); err == nil {
			return f
		}
	}
	return 60.0
}

// --- Supplier products / store products ---

func (r *Repo) ExistingSupplierProducts(ctx context.Context, partNos []string) ([]SupplierProduct, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT part_no, brand, sheet_name, supplier_category, description, availability, supplier_price, COALESCE(source_version, '') AS source_version
		FROM supplier_products WHERE part_no = ANY($1)`, partNos)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByNameLax[SupplierProduct])
}

func (r *Repo) StoreProductsFor(ctx context.Context, partNos []string) ([]StoreProduct, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT COALESCE(part_no, '') AS part_no, COALESCE(store_sku, '') AS store_sku, COALESCE(store_name, '') AS store_name,
			store_price, COALESCE(is_listed, false) AS is_listed, COALESCE(needs_review, false) AS needs_review
		FROM store_products WHERE part_no = ANY($1)`, partNos)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[StoreProduct])
}

type liveProductMatch struct {
	SKU     string  `db:"sku"`
	Barcode *string `db:"barcode"`
	Price   float64 `db:"price"`
}

func (r *Repo) LiveProductMatches(ctx context.Context, partNos []string) ([]liveProductMatch, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT sku, barcode, price::float8 AS price FROM products
		WHERE sku = ANY($1) OR barcode = ANY($1)`, partNos)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[liveProductMatch])
}

func (r *Repo) UpsertNewSupplierProducts(ctx context.Context, products []EnrichedProduct, sourceVersion string) error {
	for _, np := range products {
		if _, err := r.pool.Exec(ctx, `
			INSERT INTO supplier_products (part_no, brand, sheet_name, supplier_category, description, availability, supplier_price, source_version, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
			ON CONFLICT (part_no) DO UPDATE SET
				brand = EXCLUDED.brand,
				sheet_name = EXCLUDED.sheet_name,
				supplier_category = EXCLUDED.supplier_category,
				description = EXCLUDED.description,
				availability = EXCLUDED.availability,
				supplier_price = EXCLUDED.supplier_price,
				source_version = EXCLUDED.source_version,
				updated_at = now()`,
			np.PartNo, np.Brand, np.SheetName, np.SupplierCategory, np.Description, np.Availability, np.SupplierPrice, sourceVersion); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) UpdateSupplierPrice(ctx context.Context, partNo string, newPrice int, sourceVersion string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE supplier_products SET supplier_price = $2, source_version = $3, updated_at = now()
		WHERE part_no = $1`, partNo, newPrice, sourceVersion)
	return err
}

func (r *Repo) UpdateSupplierAvailability(ctx context.Context, partNo, availability string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE supplier_products SET availability = $2, updated_at = now()
		WHERE part_no = $1`, partNo, availability)
	return err
}

type PriceHistoryEntry struct {
	PartNo          string
	OldPrice        int
	NewPrice        int
	OldAvailability string
	NewAvailability string
	SourceVersion   string
}

func (r *Repo) InsertPriceHistory(ctx context.Context, entries []PriceHistoryEntry) error {
	for _, e := range entries {
		if _, err := r.pool.Exec(ctx, `
			INSERT INTO supplier_price_history (part_no, old_price, new_price, old_availability, new_availability, source_version)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			e.PartNo, e.OldPrice, e.NewPrice, e.OldAvailability, e.NewAvailability, e.SourceVersion); err != nil {
			return err
		}
	}
	return nil
}

// SyncStorePrice mirrors apply's "if listed in store, update cost + resale
// price with markup" write against the products table.
func (r *Repo) SyncStorePrice(ctx context.Context, sku string, costPrice, price int) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		UPDATE products SET cost_price = $2, price = $3, updated_at = now()
		WHERE sku = $1 RETURNING id::text`, sku, costPrice, price).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (r *Repo) UnpublishDiscontinued(ctx context.Context, sku string) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		UPDATE products SET stock_quantity = 0, is_published = false, updated_at = now()
		WHERE sku = $1 RETURNING id::text`, sku).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

type SupplierProductRow struct {
	PartNo            string  `db:"part_no"`
	SheetName         string  `db:"sheet_name"`
	SupplierCategory  string  `db:"supplier_category"`
	Description       string  `db:"description"`
	SupplierPrice     int     `db:"supplier_price"`
	StoreCategoryID   *string `db:"store_category_id"`
	StoreCategoryName *string `db:"store_category_name"`
}

func (r *Repo) SupplierProductByPartNo(ctx context.Context, partNo string) (*SupplierProductRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT part_no, sheet_name, supplier_category, description, supplier_price, store_category_id::text, store_category_name
		FROM supplier_products WHERE part_no = $1`, partNo)
	if err != nil {
		return nil, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[SupplierProductRow])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repo) MappingCategoryID(ctx context.Context, sheet, category string) (*string, error) {
	var id *string
	err := r.pool.QueryRow(ctx, `
		SELECT store_category_id::text FROM supplier_category_mappings
		WHERE supplier_sheet = $1 AND supplier_category = $2`, sheet, category).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return id, err
}

func (r *Repo) UpsertPublishedProduct(ctx context.Context, sku, name, description string, price, costPrice int, categoryID string, published bool) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO products (sku, name, description, price, cost_price, category_id, is_published, stock_quantity, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::uuid, $7, 10, now())
		ON CONFLICT (sku) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			price = EXCLUDED.price,
			cost_price = EXCLUDED.cost_price,
			category_id = EXCLUDED.category_id,
			is_published = EXCLUDED.is_published,
			updated_at = now()
		RETURNING id::text`,
		sku, name, description, price, costPrice, categoryID, published).Scan(&id)
	return id, err
}

func (r *Repo) UpsertStoreLink(ctx context.Context, partNo, storeSKU string, storePrice int) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO store_products (part_no, store_sku, store_price, is_listed, last_synced_at)
		VALUES ($1, $2, $3, true, now())
		ON CONFLICT (part_no) DO UPDATE SET
			store_sku = EXCLUDED.store_sku,
			store_price = EXCLUDED.store_price,
			is_listed = true,
			last_synced_at = now()`, partNo, storeSKU, storePrice)
	return err
}

// ProductPriceBySKU exists because store_products.store_price is NOT NULL —
// the old /api link handler inserted links without any price, which could
// never have satisfied that constraint.
func (r *Repo) ProductPriceBySKU(ctx context.Context, sku string) (int, error) {
	var price float64
	err := r.pool.QueryRow(ctx, `SELECT price::float8 FROM products WHERE sku = $1`, sku).Scan(&price)
	return int(price), err
}

func (r *Repo) ProductSKUExists(ctx context.Context, sku string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE sku = $1)`, sku).Scan(&exists)
	return exists, err
}

func (r *Repo) AdoptProductSKU(ctx context.Context, oldSKU, newSKU string) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		UPDATE products SET sku = $2, updated_at = now() WHERE sku = $1 RETURNING id::text`, oldSKU, newSKU).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (r *Repo) SetItemOverride(ctx context.Context, partNo, storeCategoryID, storeCategoryName string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE supplier_products SET store_category_id = $2::uuid, store_category_name = $3, updated_at = now()
		WHERE part_no = $1`, partNo, storeCategoryID, storeCategoryName)
	return err
}

// --- Catalog browse (the admin mirror-catalog page) ---

type CatalogProduct struct {
	PartNo            string  `db:"part_no" json:"part_no"`
	Brand             string  `db:"brand" json:"brand"`
	SheetName         string  `db:"sheet_name" json:"sheet_name"`
	SupplierCategory  string  `db:"supplier_category" json:"supplier_category"`
	Description       string  `db:"description" json:"description"`
	Availability      string  `db:"availability" json:"availability"`
	SupplierPrice     int     `db:"supplier_price" json:"supplier_price"`
	StoreCategoryID   *string `db:"store_category_id" json:"store_category_id,omitempty"`
	StoreCategoryName *string `db:"store_category_name" json:"store_category_name,omitempty"`
	LiveSKU           *string `db:"live_sku" json:"live_sku,omitempty"`
	MatchSKU          *string `db:"match_sku" json:"match_sku,omitempty"`
	MatchName         *string `db:"match_name" json:"match_name,omitempty"`
}

// CatalogPage folds what used to be three separate browser round-trip
// patterns (paged supplier_products, a per-row products ILIKE N+1, and a
// store_products IN-lookup) into one query: the live-SKU join replaces the
// IN-lookup and the LATERAL replaces the N+1 similarity probe.
func (r *Repo) CatalogPage(ctx context.Context, search, brand string, page, pageSize int) ([]CatalogProduct, int, error) {
	where := "WHERE 1=1"
	var args []any
	if search != "" {
		args = append(args, "%"+search+"%")
		where += fmt.Sprintf(" AND (sp.part_no ILIKE $%d OR sp.description ILIKE $%d)", len(args), len(args))
	}
	if brand != "" && brand != "All" {
		args = append(args, brand)
		where += fmt.Sprintf(" AND sp.brand = $%d", len(args))
	}

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM supplier_products sp `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, pageSize, (page-1)*pageSize)
	limitParam := strconv.Itoa(len(args) - 1)
	offsetParam := strconv.Itoa(len(args))
	rows, err := r.pool.Query(ctx, `
		SELECT sp.part_no, sp.brand, sp.sheet_name, sp.supplier_category, sp.description, sp.availability,
			sp.supplier_price, sp.store_category_id::text, sp.store_category_name,
			st.store_sku AS live_sku, pm.sku AS match_sku, pm.name AS match_name
		FROM supplier_products sp
		LEFT JOIN store_products st ON st.part_no = sp.part_no AND st.is_listed
		LEFT JOIN LATERAL (
			SELECT sku, name FROM products WHERE name ILIKE '%' || left(sp.description, 20) || '%' LIMIT 1
		) pm ON true
		`+where+`
		ORDER BY sp.brand ASC, sp.supplier_category ASC
		LIMIT $`+limitParam+` OFFSET $`+offsetParam, args...)
	if err != nil {
		return nil, 0, err
	}
	products, err := pgx.CollectRows(rows, pgx.RowToStructByName[CatalogProduct])
	return products, total, err
}
