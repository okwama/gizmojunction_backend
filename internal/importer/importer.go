// Package importer is the Go port of the import-job Deno Edge Function:
// CSV product imports with brand find-or-create, the smart category
// resolver (alias map → name inference → Gemini fallback), image
// re-hosting to R2, and sku-based product upserts. Ported because products
// now live in Neon — the Deno function kept writing them into Supabase.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gizmojunction/backend/internal/auth"
	"gizmojunction/backend/internal/storage"
)

type Config struct {
	GeminiAPIKey     string
	BackendPublicURL string
}

type Importer struct {
	pool    *pgxpool.Pool
	authSvc *auth.Service
	store   *storage.Client // nil disables image re-hosting
	cfg     Config
}

func Register(api huma.API, pool *pgxpool.Pool, authSvc *auth.Service, store *storage.Client, cfg Config) {
	imp := &Importer{pool: pool, authSvc: authSvc, store: store, cfg: cfg}

	huma.Register(api, huma.Operation{
		OperationID: "admin-create-import-job",
		Method:      http.MethodPost,
		Path:        "/v1/admin/import/jobs",
		Summary:     "Create an import job record (admin only)",
	}, imp.CreateJob)

	huma.Register(api, huma.Operation{
		OperationID: "admin-run-import",
		Method:      http.MethodPost,
		Path:        "/v1/admin/import/run",
		Summary:     "Import a batch of products (admin only)",
	}, imp.Run)
}

// --- Job creation ---

type CreateJobInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Filename  string `json:"filename"`
		TotalRows int    `json:"total_rows"`
	}
}

type CreateJobOutput struct {
	Body struct {
		ID string `json:"id"`
	}
}

func (imp *Importer) CreateJob(ctx context.Context, input *CreateJobInput) (*CreateJobOutput, error) {
	if _, err := imp.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	out := &CreateJobOutput{}
	err := imp.pool.QueryRow(ctx, `
		INSERT INTO import_jobs (filename, total_rows, status)
		VALUES ($1, $2, 'processing')
		RETURNING id::text`, input.Body.Filename, input.Body.TotalRows).Scan(&out.Body.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to create import job", err)
	}
	return out, nil
}

// --- Batch import ---

type ImportProduct struct {
	Name            string   `json:"name,omitempty"`
	SKU             string   `json:"sku"`
	Price           *float64 `json:"price,omitempty"`
	SalePrice       *float64 `json:"sale_price,omitempty"`
	BrandName       string   `json:"brand_name,omitempty"`
	Category        string   `json:"category,omitempty"`
	ImageURL        string   `json:"image_url,omitempty"`
	DescriptionHTML string   `json:"description_html,omitempty"`
	SummaryHTML     string   `json:"summary_html,omitempty"`
	Barcode         *string  `json:"barcode,omitempty"`
	Tags            *string  `json:"tags,omitempty"`
	StockQty        *int32   `json:"stock_quantity,omitempty"`
}

type RunInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Products []ImportProduct `json:"products"`
		JobID    string          `json:"jobId,omitempty"`
		Strict   bool            `json:"strict,omitempty"`
		Mode     string          `json:"mode,omitempty"` // "" | "update"
	}
}

type RunOutput struct {
	Body struct {
		Success      bool     `json:"success"`
		Imported     int      `json:"imported"`
		Errors       int      `json:"errors"`
		ErrorDetails []string `json:"errorDetails"`
	}
}

var numericName = regexp.MustCompile(`^\d+$`)

func (imp *Importer) Run(ctx context.Context, input *RunInput) (*RunOutput, error) {
	if _, err := imp.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.Products) == 0 {
		return nil, huma.Error400BadRequest("Invalid products data")
	}

	out := &RunOutput{}
	out.Body.ErrorDetails = []string{}

	for _, row := range input.Body.Products {
		if row.Price != nil && *row.Price <= 0 {
			out.Body.Errors++
			out.Body.ErrorDetails = append(out.Body.ErrorDetails, fmt.Sprintf("SKU %s skipped: Invalid price (%v)", row.SKU, *row.Price))
			continue
		}
		if row.Name != "" && numericName.MatchString(strings.TrimSpace(row.Name)) {
			out.Body.Errors++
			out.Body.ErrorDetails = append(out.Body.ErrorDetails, fmt.Sprintf("SKU %s skipped: Purely numerical name (%q)", row.SKU, row.Name))
			continue
		}

		if input.Body.Mode == "update" {
			var exists bool
			if err := imp.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE sku = $1)`, row.SKU).Scan(&exists); err != nil {
				out.Body.Errors++
				out.Body.ErrorDetails = append(out.Body.ErrorDetails, fmt.Sprintf("SKU %s: lookup failed - %v", row.SKU, err))
				continue
			}
			if !exists {
				continue // update mode skips unknown SKUs, matching the original
			}
		}

		if err := imp.importOne(ctx, row, input.Body.JobID, input.Body.Mode); err != nil {
			out.Body.Errors++
			out.Body.ErrorDetails = append(out.Body.ErrorDetails, fmt.Sprintf("SKU %s: %v", row.SKU, err))
			continue
		}
		out.Body.Imported++
	}

	if input.Body.JobID != "" {
		_, err := imp.pool.Exec(ctx, `
			UPDATE import_jobs SET imported = imported + $2, errors = errors + $3, updated_at = now()
			WHERE id = $1`, input.Body.JobID, out.Body.Imported, out.Body.Errors)
		if err != nil {
			log.Printf("importer: failed to update job %s: %v", input.Body.JobID, err)
		}
	}

	out.Body.Success = true
	return out, nil
}

func (imp *Importer) importOne(ctx context.Context, row ImportProduct, jobID, mode string) error {
	var brandID *string
	if row.BrandName != "" {
		id, err := imp.findOrCreateBrand(ctx, row.BrandName)
		if err != nil {
			return fmt.Errorf("brand: %w", err)
		}
		brandID = &id
	}

	var categoryID *string
	if mode != "update" {
		id, err := imp.resolveCategory(ctx, row.Category, row.Name)
		if err != nil {
			return fmt.Errorf("category: %w", err)
		}
		categoryID = id
	}

	imageURL := row.ImageURL
	if mode != "update" {
		// Preserve the existing image when the CSV has none.
		if imageURL == "" {
			_ = imp.pool.QueryRow(ctx, `SELECT COALESCE(image_url, '') FROM products WHERE sku = $1`, row.SKU).Scan(&imageURL)
		}
		imageURL = imp.maybeRehostImage(ctx, imageURL)
	}

	var tags []string
	if row.Tags != nil && strings.TrimSpace(*row.Tags) != "" {
		for _, t := range strings.Split(*row.Tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
	}

	var productID string
	err := imp.pool.QueryRow(ctx, `
		INSERT INTO products (sku, name, price, sale_price, brand_id, category_id, image_url, description_html, summary_html, barcode, tags, stock_quantity, import_job_id, updated_at)
		VALUES ($1, $2, $3, $4, $5::uuid, $6::uuid, $7, $8, $9, $10, $11, $12, $13::uuid, now())
		ON CONFLICT (sku) DO UPDATE SET
			name = COALESCE(EXCLUDED.name, products.name),
			price = COALESCE(EXCLUDED.price, products.price),
			sale_price = COALESCE(EXCLUDED.sale_price, products.sale_price),
			brand_id = COALESCE(EXCLUDED.brand_id, products.brand_id),
			category_id = COALESCE(EXCLUDED.category_id, products.category_id),
			image_url = COALESCE(EXCLUDED.image_url, products.image_url),
			description_html = COALESCE(EXCLUDED.description_html, products.description_html),
			summary_html = COALESCE(EXCLUDED.summary_html, products.summary_html),
			barcode = COALESCE(EXCLUDED.barcode, products.barcode),
			tags = COALESCE(EXCLUDED.tags, products.tags),
			stock_quantity = COALESCE(EXCLUDED.stock_quantity, products.stock_quantity),
			import_job_id = COALESCE(EXCLUDED.import_job_id, products.import_job_id),
			updated_at = now()
		RETURNING id::text`,
		row.SKU, nullIfEmpty(row.Name), row.Price, row.SalePrice, brandID, categoryID,
		nullIfEmpty(imageURL), nullIfEmpty(row.DescriptionHTML), nullIfEmpty(row.SummaryHTML),
		row.Barcode, tags, row.StockQty, nullIfEmpty(jobID)).Scan(&productID)
	return err
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (imp *Importer) findOrCreateBrand(ctx context.Context, name string) (string, error) {
	var id string
	err := imp.pool.QueryRow(ctx, `SELECT id::text FROM brands WHERE name = $1 LIMIT 1`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	slug := strings.ReplaceAll(strings.ToLower(name), " ", "-")
	err = imp.pool.QueryRow(ctx, `INSERT INTO brands (name, slug) VALUES ($1, $2) RETURNING id::text`, name, slug).Scan(&id)
	return id, err
}

// --- Smart category resolver (ported from the Deno function) ---

var categoryAliases = map[string][]string{
	"Computing":            {"Laptops", "Desktops", "Monitors", "Components", "PC", "Computers", "Hardware", "All-In-One"},
	"Mobile & Tablets":     {"Smartphones", "Tablets", "Wearables", "Phones", "Mobiles", "Cell Phones", "iPads", "Android"},
	"Audio & Music":        {"Headphones", "Speakers", "Bluetooth Speakers", "Audio", "Home Audio", "Earbuds", "Soundbars"},
	"TV & Entertainment":   {"Televisions", "TV", "Streaming", "Media Players", "Projectors", "Home Theater"},
	"Gaming":               {"Consoles", "Gaming Gear", "Gaming Peripherals", "PlayStation", "Xbox", "Nintendo", "Gaming"},
	"Networking & Storage": {"Networking", "External Storage", "Hard Drives", "SSD", "Routers", "Switches", "NAS"},
	"Printers & Office":    {"Printers", "Scanners", "Supplies", "Inks", "Toners", "Office Equipment"},
	"Smart Home & Security": {"Security", "Smart Home", "Cameras", "Drones", "CCTV", "Automation"},
}

var slugCleaner = regexp.MustCompile(`[^a-z0-9]+`)

func (imp *Importer) resolveCategory(ctx context.Context, rawCat, productName string) (*string, error) {
	var parts []string
	for _, p := range strings.Split(rawCat, ">") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}

	// Rule A: infer from product name (keywords, then Gemini) when missing.
	if len(parts) == 0 && productName != "" {
		nameLower := strings.ToLower(productName)
		for root, keywords := range categoryAliases {
			for _, k := range keywords {
				if strings.Contains(nameLower, strings.ToLower(k)) {
					parts = []string{root}
					break
				}
			}
			if len(parts) > 0 {
				break
			}
		}
		if len(parts) == 0 {
			if aiCategory := imp.askGeminiForCategory(ctx, productName); aiCategory != "" {
				if _, known := categoryAliases[aiCategory]; known {
					parts = []string{aiCategory}
				}
			}
		}
	}

	// Rule B: prepend the canonical root when a known subcategory arrives
	// at the top level.
	if len(parts) == 1 {
		for root, keywords := range categoryAliases {
			for _, k := range keywords {
				if strings.EqualFold(k, parts[0]) {
					if !strings.EqualFold(parts[0], root) {
						parts = []string{root, parts[0]}
					}
					break
				}
			}
		}
	}

	if len(parts) == 0 {
		parts = []string{"Others"}
	}

	var parentID *string
	for _, part := range parts {
		var id string
		err := imp.pool.QueryRow(ctx, `
			SELECT id::text FROM categories
			WHERE name = $1 AND parent_id IS NOT DISTINCT FROM $2::uuid
			LIMIT 1`, part, parentID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			baseSlug := strings.Trim(slugCleaner.ReplaceAllString(strings.ToLower(part), "-"), "-")
			slug := fmt.Sprintf("%s-%s", baseSlug, randSuffix())
			err = imp.pool.QueryRow(ctx, `
				INSERT INTO categories (name, slug, parent_id)
				VALUES ($1, $2, $3::uuid)
				RETURNING id::text`, part, slug, parentID).Scan(&id)
		}
		if err != nil {
			return nil, err
		}
		parentID = &id
	}
	return parentID, nil
}

func randSuffix() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

func (imp *Importer) askGeminiForCategory(ctx context.Context, productName string) string {
	if imp.cfg.GeminiAPIKey == "" {
		return ""
	}
	prompt := fmt.Sprintf(`Analyze this product name and suggest the best category from this list: Computing, Mobile & Tablets, Audio & Music, TV & Entertainment, Gaming, Networking & Storage, Printers & Office, Smart Home & Security. Return ONLY the category name. Product Name: %q`, productName)
	body, _ := json.Marshal(map[string]any{
		"contents": []map[string]any{{"parts": []map[string]any{{"text": prompt}}}},
	})
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		"https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key="+imp.cfg.GeminiAPIKey,
		strings.NewReader(string(body)))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.NewDecoder(resp.Body).Decode(&parsed) != nil {
		return ""
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parsed.Candidates[0].Content.Parts[0].Text)
}

// --- Image re-hosting ---

var privateHost = regexp.MustCompile(`^(127\.|10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[0-1])\.)`)

// maybeRehostImage downloads an external product image and stores it in R2,
// returning the stable /v1/documents proxy URL — or the original URL when
// anything about the download looks wrong, exactly like the Deno version.
func (imp *Importer) maybeRehostImage(ctx context.Context, imageURL string) string {
	if imageURL == "" || imp.store == nil || imp.cfg.BackendPublicURL == "" {
		return imageURL
	}
	if !strings.HasPrefix(imageURL, "http://") && !strings.HasPrefix(imageURL, "https://") {
		return imageURL
	}
	parsed, err := url.Parse(imageURL)
	if err != nil {
		return imageURL
	}
	if privateHost.MatchString(parsed.Hostname()) {
		log.Printf("importer: blocked SSRF attempt to internal host: %s", parsed.Hostname())
		return ""
	}
	if strings.Contains(imageURL, "supabase.co/storage") || strings.Contains(imageURL, imp.cfg.BackendPublicURL+"/v1/documents/") {
		return imageURL // already hosted with us
	}

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, imageURL, nil)
	if err != nil {
		return imageURL
	}
	req.Header.Set("User-Agent", "GizmoJunction-Bot/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return imageURL
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		return imageURL
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024+1))
	if err != nil || len(data) > 5*1024*1024 {
		return imageURL
	}

	filePath := fmt.Sprintf("products/product-%d-%s.webp", time.Now().UnixMilli(), randSuffix())
	if err := imp.store.Upload(ctx, filePath, data, "image/webp"); err != nil {
		log.Printf("importer: image upload failed, keeping original URL: %v", err)
		return imageURL
	}
	return imp.cfg.BackendPublicURL + "/v1/documents/" + filePath
}
