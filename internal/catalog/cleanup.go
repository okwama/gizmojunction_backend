package catalog

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
)

// The AI Product Cleanup page: filtered "products needing attention" lists
// and a bulk apply that previously ran as a per-product loop of category
// find-or-create inserts + partial updates from the browser — now one
// request, with all lookups server-side.

type CleanupProduct struct {
	ID              string  `db:"id" json:"id"`
	Name            string  `db:"name" json:"name"`
	SKU             *string `db:"sku" json:"sku,omitempty"`
	CategoryID      *string `db:"category_id" json:"category_id,omitempty"`
	CategoryName    *string `db:"category_name" json:"category_name,omitempty"`
	BrandName       *string `db:"brand_name" json:"brand_name,omitempty"`
	SummaryHTML     *string `db:"summary_html" json:"summary_html,omitempty"`
	DescriptionHTML *string `db:"description_html" json:"description_html,omitempty"`
	ImageURL        *string `db:"image_url" json:"image_url,omitempty"`
}

const cleanupProductColumns = `p.id::text, p.name, p.sku, p.category_id::text, c.name AS category_name, b.name AS brand_name, p.summary_html, p.description_html, p.image_url`

func (r *Repo) ListCleanupProducts(ctx context.Context, filter string) ([]CleanupProduct, error) {
	var where string
	switch filter {
	case "generic":
		where = `p.name ILIKE '%generic%'`
	case "others":
		where = `c.name = 'Others'`
	case "uncategorized":
		where = `p.category_id IS NULL`
	case "descriptions":
		where = `p.description_html IS NULL OR p.description_html = '' OR p.summary_html IS NULL OR p.summary_html = ''`
	case "sku":
		where = `p.sku IS NULL OR length(p.sku) > 20 OR p.sku LIKE '% %'`
	default:
		return nil, fmt.Errorf("unknown cleanup filter %q", filter)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+cleanupProductColumns+`
		FROM products p
		LEFT JOIN categories c ON c.id = p.category_id
		LEFT JOIN brands b ON b.id = p.brand_id
		WHERE `+where+`
		ORDER BY p.name ASC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CleanupProduct])
}

type CleanupUpdate struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	SKU             string `json:"sku,omitempty"`
	SummaryHTML     string `json:"summary_html,omitempty"`
	DescriptionHTML string `json:"description_html,omitempty"`
	RootCategory    string `json:"root_category,omitempty"`
	SubCategory     string `json:"sub_category,omitempty"`
}

var slugCleaner = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	return strings.Trim(slugCleaner.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// findOrCreateCategory matches by case-insensitive name within the given
// parent (nil = root level), creating with a generated slug when missing —
// the same semantics the cleanup page previously implemented client-side.
func (r *Repo) findOrCreateCategory(ctx context.Context, name string, parentID *string) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		SELECT id::text FROM categories
		WHERE lower(name) = lower($1) AND parent_id IS NOT DISTINCT FROM $2::uuid
		LIMIT 1`, name, parentID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	err = r.pool.QueryRow(ctx, `
		INSERT INTO categories (name, slug, parent_id)
		VALUES ($1, $2, $3::uuid)
		RETURNING id::text`, name, slugify(name), parentID).Scan(&id)
	return id, err
}

// ApplyCleanupUpdate resolves the suggested category pair (if any) and
// applies only the provided fields. Returns the product id so callers can
// reindex search.
func (r *Repo) ApplyCleanupUpdate(ctx context.Context, u CleanupUpdate) error {
	var categoryID *string
	if u.RootCategory != "" && u.SubCategory != "" {
		rootID, err := r.findOrCreateCategory(ctx, u.RootCategory, nil)
		if err != nil {
			return fmt.Errorf("resolve root category: %w", err)
		}
		subID, err := r.findOrCreateCategory(ctx, u.SubCategory, &rootID)
		if err != nil {
			return fmt.Errorf("resolve subcategory: %w", err)
		}
		categoryID = &subID
	}

	_, err := r.pool.Exec(ctx, `
		UPDATE products SET
			name = COALESCE($2, name),
			sku = COALESCE($3, sku),
			summary_html = COALESCE($4, summary_html),
			description_html = COALESCE($5, description_html),
			category_id = COALESCE($6::uuid, category_id),
			updated_at = now()
		WHERE id = $1`,
		u.ID, nullIfEmpty(u.Name), nullIfEmpty(u.SKU), nullIfEmpty(u.SummaryHTML), nullIfEmpty(u.DescriptionHTML), categoryID)
	return err
}

// PatchDescriptions fills blank product descriptions from a CSV in one
// set-based statement — the old admin component did two queries per row
// from the browser. Rows whose product already has a description are
// counted but left untouched.
type PatchDescriptionsResult struct {
	Updated        int `json:"updated"`
	NotFound       int `json:"not_found"`
	SkippedHasDesc int `json:"skipped_has_desc"`
}

func (r *Repo) PatchDescriptions(ctx context.Context, skus, descriptions []string) (PatchDescriptionsResult, error) {
	var res PatchDescriptionsResult
	err := r.pool.QueryRow(ctx, `
		WITH input AS (
			SELECT * FROM unnest($1::text[], $2::text[]) AS t(sku, description)
		),
		matched AS (
			SELECT i.sku, i.description, p.id, p.description AS existing
			FROM input i JOIN products p ON p.sku = i.sku
		),
		updated AS (
			UPDATE products p SET description = m.description, updated_at = now()
			FROM matched m
			WHERE p.id = m.id AND (m.existing IS NULL OR btrim(m.existing) = '')
			RETURNING p.id
		)
		SELECT
			(SELECT count(*) FROM updated),
			(SELECT count(*) FROM input) - (SELECT count(*) FROM matched),
			(SELECT count(*) FROM matched) - (SELECT count(*) FROM updated)`,
		skus, descriptions).Scan(&res.Updated, &res.NotFound, &res.SkippedHasDesc)
	return res, err
}

type PatchDescriptionsInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Items []struct {
			SKU         string `json:"sku"`
			Description string `json:"description"`
		} `json:"items"`
	}
}

func (h *AdminHandlers) PatchDescriptions(ctx context.Context, input *PatchDescriptionsInput) (*struct{ Body PatchDescriptionsResult }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.Items) == 0 {
		return nil, huma.Error400BadRequest("items is required")
	}
	skus := make([]string, 0, len(input.Body.Items))
	descriptions := make([]string, 0, len(input.Body.Items))
	for _, item := range input.Body.Items {
		skus = append(skus, item.SKU)
		descriptions = append(descriptions, item.Description)
	}
	result, err := h.repo.PatchDescriptions(ctx, skus, descriptions)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to patch descriptions", err)
	}
	return &struct{ Body PatchDescriptionsResult }{Body: result}, nil
}

type ListCleanupProductsInput struct {
	Authorization string `header:"Authorization"`
	Filter        string `query:"filter" enum:"generic,others,uncategorized,descriptions,sku"`
}

func (h *AdminHandlers) ListCleanupProducts(ctx context.Context, input *ListCleanupProductsInput) (*struct{ Body []CleanupProduct }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	products, err := h.repo.ListCleanupProducts(ctx, input.Filter)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load cleanup products", err)
	}
	return &struct{ Body []CleanupProduct }{Body: products}, nil
}

type ApplyCleanupInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Updates []CleanupUpdate `json:"updates"`
	}
}

type ApplyCleanupOutput struct {
	Body struct {
		Applied int      `json:"applied"`
		Errors  []string `json:"errors,omitempty"`
	}
}

// ApplyCleanup keeps the old loop's continue-on-error behavior: one bad
// product doesn't abort the batch, failures are reported back per product.
func (h *AdminHandlers) ApplyCleanup(ctx context.Context, input *ApplyCleanupInput) (*ApplyCleanupOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}

	out := &ApplyCleanupOutput{}
	for _, u := range input.Body.Updates {
		if u.ID == "" {
			out.Body.Errors = append(out.Body.Errors, "update missing product id")
			continue
		}
		if err := h.repo.ApplyCleanupUpdate(ctx, u); err != nil {
			out.Body.Errors = append(out.Body.Errors, fmt.Sprintf("%s: %v", u.ID, err))
			continue
		}
		out.Body.Applied++
	}
	return out, nil
}
