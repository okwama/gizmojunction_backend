package suppliersync

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"gizmojunction/backend/internal/auth"
)

type Handlers struct {
	repo    *Repo
	authSvc *auth.Service
}

// Register wires the huma endpoints plus the multipart upload handler,
// which lives on the raw mux because file uploads are simpler outside huma.
func Register(api huma.API, mux *http.ServeMux, repo *Repo, authSvc *auth.Service) {
	h := &Handlers{repo: repo, authSvc: authSvc}

	mux.HandleFunc("/v1/admin/supplier-sync/upload", h.handleUpload)

	huma.Register(api, huma.Operation{
		OperationID: "admin-supplier-sync-apply",
		Method:      http.MethodPost,
		Path:        "/v1/admin/supplier-sync/apply",
		Summary:     "Apply a pending sync session's diff to the mirror catalog (admin only)",
	}, h.Apply)

	huma.Register(api, huma.Operation{
		OperationID: "admin-supplier-sync-publish",
		Method:      http.MethodPost,
		Path:        "/v1/admin/supplier-sync/publish",
		Summary:     "Publish supplier products into the store catalog (admin only)",
	}, h.Publish)

	huma.Register(api, huma.Operation{
		OperationID: "admin-supplier-sync-link",
		Method:      http.MethodPost,
		Path:        "/v1/admin/supplier-sync/link",
		Summary:     "Link a supplier part number to an existing store product (admin only)",
	}, h.Link)

	huma.Register(api, huma.Operation{
		OperationID: "admin-supplier-sync-list-mappings",
		Method:      http.MethodGet,
		Path:        "/v1/admin/supplier-sync/mappings",
		Summary:     "List supplier category mappings (admin only)",
	}, h.ListMappings)

	huma.Register(api, huma.Operation{
		OperationID: "admin-supplier-sync-save-mapping",
		Method:      http.MethodPost,
		Path:        "/v1/admin/supplier-sync/mappings",
		Summary:     "Map, ignore or unignore a supplier category (admin only)",
	}, h.SaveMapping)

	huma.Register(api, huma.Operation{
		OperationID: "admin-supplier-sync-list-settings",
		Method:      http.MethodGet,
		Path:        "/v1/admin/supplier-sync/settings",
		Summary:     "List sync settings (admin only)",
	}, h.ListSettings)

	huma.Register(api, huma.Operation{
		OperationID: "admin-supplier-sync-save-setting",
		Method:      http.MethodPost,
		Path:        "/v1/admin/supplier-sync/settings",
		Summary:     "Upsert a sync setting (admin only)",
	}, h.SaveSetting)

	huma.Register(api, huma.Operation{
		OperationID: "admin-supplier-sync-override",
		Method:      http.MethodPost,
		Path:        "/v1/admin/supplier-sync/override",
		Summary:     "Set an item-level category override on a supplier product (admin only)",
	}, h.Override)

	huma.Register(api, huma.Operation{
		OperationID: "admin-supplier-sync-catalog",
		Method:      http.MethodGet,
		Path:        "/v1/admin/supplier-sync/catalog",
		Summary:     "Browse the supplier mirror catalog with live-listing and similarity info (admin only)",
	}, h.Catalog)

	huma.Register(api, huma.Operation{
		OperationID: "admin-supplier-sync-sessions",
		Method:      http.MethodGet,
		Path:        "/v1/admin/supplier-sync/sessions",
		Summary:     "List sync session history (admin only)",
	}, h.Sessions)
}

// --- Upload (multipart, raw mux) ---

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *Handlers) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if _, err := h.authSvc.RequireRole(r.Header.Get("Authorization"), "ADMIN"); err != nil {
		status := http.StatusUnauthorized
		if se, ok := err.(huma.StatusError); ok {
			status = se.GetStatus()
		}
		writeJSON(w, status, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

func (h *Handlers) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()

	switch r.Method {
	case http.MethodGet:
		session, err := h.repo.PendingSession(ctx)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Database error checking sessions"})
			return
		}
		if session == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"session": nil})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"session_id": session.ID, "diff_report": session.DiffReport})

	case http.MethodDelete:
		if err := h.repo.DiscardPendingSessions(ctx); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to discard session"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})

	case http.MethodPost:
		h.handleUploadPost(w, r)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (h *Handlers) handleUploadPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pending, err := h.repo.PendingSession(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Database error checking sessions"})
		return
	}
	if pending != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "A pending sync session already exists. Please discard it before uploading a new file."})
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Could not parse form"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "File 'file' is missing"})
		return
	}
	defer file.Close()

	if !strings.HasSuffix(header.Filename, ".xlsx") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Only .xlsx files are allowed"})
		return
	}
	sourceVersion := strings.TrimSuffix(header.Filename, ".xlsx")

	incoming, err := ParseSupplierExcel(file, sourceVersion)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("Failed to parse Excel: %v", err)})
		return
	}

	mappings, err := h.repo.mappingsAsDomain(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to fetch category mappings"})
		return
	}

	resolution := ResolveCategories(incoming, mappings)
	if len(resolution.UnmappedCategories) > 0 {
		if err := h.repo.SeedUnmappedMappings(ctx, resolution.UnmappedCategories); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to persist new categories: %v", err)})
			return
		}
	}

	partNos := make([]string, 0, len(incoming))
	for _, p := range incoming {
		partNos = append(partNos, p.PartNo)
	}

	existing, err := h.repo.ExistingSupplierProducts(ctx, partNos)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to fetch existing products"})
		return
	}
	storeProducts, err := h.repo.StoreProductsFor(ctx, partNos)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to fetch store mappings"})
		return
	}

	// Direct sku/barcode matches against the live catalog, merged in when
	// the bridge table doesn't already know the part number.
	liveMatches, err := h.repo.LiveProductMatches(ctx, partNos)
	if err != nil {
		log.Printf("suppliersync: live product search error: %v", err)
	}
	matched := make(map[string]bool, len(storeProducts))
	for _, sp := range storeProducts {
		matched[sp.PartNo] = true
	}
	for _, lp := range liveMatches {
		for _, ip := range incoming {
			barcode := ""
			if lp.Barcode != nil {
				barcode = *lp.Barcode
			}
			if (ip.PartNo == lp.SKU || ip.PartNo == barcode) && !matched[ip.PartNo] {
				storeProducts = append(storeProducts, StoreProduct{
					PartNo:     ip.PartNo,
					StoreSKU:   lp.SKU,
					StorePrice: int(lp.Price),
					IsListed:   true,
				})
				matched[ip.PartNo] = true
			}
		}
	}

	report := BuildDiffReport(
		resolution.EnrichedProducts, existing, storeProducts,
		sourceVersion, resolution.UnmappedCategories, resolution.HasBlockingUnmapped,
	)

	sessionID, err := h.repo.InsertSession(ctx, sourceVersion, report)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to create sync session: %v", err)})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "diff_report": report})
}

// --- Apply ---

type ApplyInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		SessionID string `json:"session_id"`
	}
}

type ApplyOutput struct {
	Body struct {
		Success   bool        `json:"success"`
		AppliedAt string      `json:"applied_at"`
		Summary   DiffSummary `json:"summary"`
	}
}

func (h *Handlers) Apply(ctx context.Context, input *ApplyInput) (*ApplyOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.SessionID == "" {
		return nil, huma.Error400BadRequest("missing session_id")
	}

	session, err := h.repo.SessionByID(ctx, input.Body.SessionID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load session", err)
	}
	if session == nil {
		return nil, huma.Error404NotFound("Session not found")
	}
	if session.Status != "pending" {
		return nil, huma.Error409Conflict("Session already applied or discarded")
	}

	var report DiffReport
	if err := json.Unmarshal(session.DiffReport, &report); err != nil {
		return nil, huma.Error500InternalServerError("corrupt diff report", err)
	}

	if len(report.NewProducts) > 0 {
		if err := h.repo.UpsertNewSupplierProducts(ctx, report.NewProducts, report.SourceVersion); err != nil {
			return nil, huma.Error500InternalServerError("Failed to upsert new products", err)
		}
	}

	markupMultiplier := 1 + h.repo.DefaultMarkup(ctx)/100

	if len(report.PriceChanges) > 0 {
		var history []PriceHistoryEntry
		for _, pc := range report.PriceChanges {
			if err := h.repo.UpdateSupplierPrice(ctx, pc.PartNo, pc.NewPrice, report.SourceVersion); err != nil {
				return nil, huma.Error500InternalServerError("Failed to update supplier prices", err)
			}
			history = append(history, PriceHistoryEntry{
				PartNo: pc.PartNo, OldPrice: pc.OldPrice, NewPrice: pc.NewPrice,
				OldAvailability: pc.Availability, NewAvailability: pc.Availability,
				SourceVersion: report.SourceVersion,
			})
			if pc.IsStoreListed {
				// The old code matched products on part_no directly, which
				// missed links whose store SKU differs — prefer the linked SKU.
				sku := pc.StoreSKU
				if sku == "" {
					sku = pc.PartNo
				}
				newStorePrice := int(float64(pc.NewPrice) * markupMultiplier)
				if _, err := h.repo.SyncStorePrice(ctx, sku, pc.NewPrice, newStorePrice); err != nil {
					log.Printf("suppliersync: failed to sync store price for %s: %v", sku, err)
				}
			}
		}
		if err := h.repo.InsertPriceHistory(ctx, history); err != nil {
			return nil, huma.Error500InternalServerError("Failed to save price history", err)
		}
	}

	if len(report.AvailabilityChanges) > 0 {
		var history []PriceHistoryEntry
		for _, ac := range report.AvailabilityChanges {
			if err := h.repo.UpdateSupplierAvailability(ctx, ac.PartNo, ac.NewAvailability); err != nil {
				return nil, huma.Error500InternalServerError("Failed to update supplier availability", err)
			}
			history = append(history, PriceHistoryEntry{
				PartNo: ac.PartNo, OldPrice: ac.SupplierPrice, NewPrice: ac.SupplierPrice,
				OldAvailability: ac.OldAvailability, NewAvailability: ac.NewAvailability,
				SourceVersion: report.SourceVersion,
			})
		}
		if err := h.repo.InsertPriceHistory(ctx, history); err != nil {
			return nil, huma.Error500InternalServerError("Failed to save availability history", err)
		}
	}

	for _, dc := range report.Discontinued {
		if err := h.repo.UpdateSupplierAvailability(ctx, dc.PartNo, "Discontinued"); err != nil {
			return nil, huma.Error500InternalServerError("Failed to update discontinued products", err)
		}
		if dc.IsStoreListed {
			sku := dc.StoreSKU
			if sku == "" {
				sku = dc.PartNo
			}
			if _, err := h.repo.UnpublishDiscontinued(ctx, sku); err != nil {
				log.Printf("suppliersync: failed to unpublish discontinued %s: %v", sku, err)
			}
		}
	}

	appliedAt, err := h.repo.MarkSessionApplied(ctx, input.Body.SessionID)
	if err != nil {
		return nil, huma.Error500InternalServerError("Failed to update sync session status", err)
	}

	out := &ApplyOutput{}
	out.Body.Success = true
	out.Body.AppliedAt = appliedAt.Format("2006-01-02T15:04:05Z07:00")
	out.Body.Summary = report.Summary
	return out, nil
}

// --- Publish ---

type PublishInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		PartNos []string `json:"part_nos"`
		Status  string   `json:"status" enum:"draft,published"`
	}
}

type PublishOutput struct {
	Body struct {
		Success      bool     `json:"success"`
		SuccessCount int      `json:"success_count"`
		Errors       []string `json:"errors"`
	}
}

func (h *Handlers) Publish(ctx context.Context, input *PublishInput) (*PublishOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.PartNos) == 0 {
		return nil, huma.Error400BadRequest("part_nos is required")
	}

	markupMultiplier := 1 + h.repo.DefaultMarkup(ctx)/100

	out := &PublishOutput{}
	out.Body.Errors = []string{}
	for _, partNo := range input.Body.PartNos {
		p, err := h.repo.SupplierProductByPartNo(ctx, partNo)
		if err != nil {
			out.Body.Errors = append(out.Body.Errors, fmt.Sprintf("%s: lookup failed - %v", partNo, err))
			continue
		}
		if p == nil {
			out.Body.Errors = append(out.Body.Errors, fmt.Sprintf("%s: Not found in mirror catalog", partNo))
			continue
		}

		categoryID := p.StoreCategoryID
		if categoryID == nil {
			categoryID, err = h.repo.MappingCategoryID(ctx, p.SheetName, p.SupplierCategory)
			if err != nil {
				out.Body.Errors = append(out.Body.Errors, fmt.Sprintf("%s: mapping lookup failed - %v", partNo, err))
				continue
			}
			if categoryID == nil {
				out.Body.Errors = append(out.Body.Errors, fmt.Sprintf("%s: Category not mapped", partNo))
				continue
			}
		}

		storePrice := int(float64(p.SupplierPrice) * markupMultiplier)
		if _, err := h.repo.UpsertPublishedProduct(ctx, p.PartNo, p.Description, p.Description, storePrice, p.SupplierPrice, *categoryID, input.Body.Status == "published"); err != nil {
			out.Body.Errors = append(out.Body.Errors, fmt.Sprintf("%s: Failed to publish to store - %v", partNo, err))
			continue
		}

		if err := h.repo.UpsertStoreLink(ctx, p.PartNo, p.PartNo, storePrice); err != nil {
			log.Printf("suppliersync: failed to upsert store link for %s: %v", partNo, err)
		}
		out.Body.SuccessCount++
	}

	out.Body.Success = true
	return out, nil
}

// --- Link ---

type LinkInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		PartNo   string `json:"part_no"`
		StoreSKU string `json:"store_sku"`
		AdoptSKU bool   `json:"adopt_sku,omitempty"`
	}
}

type successOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

func (h *Handlers) Link(ctx context.Context, input *LinkInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.PartNo == "" || input.Body.StoreSKU == "" {
		return nil, huma.Error400BadRequest("part_no and store_sku are required")
	}

	exists, err := h.repo.ProductSKUExists(ctx, input.Body.StoreSKU)
	if err != nil {
		return nil, huma.Error500InternalServerError("lookup failed", err)
	}
	if !exists {
		return nil, huma.Error404NotFound("Store SKU not found in products table")
	}

	price, err := h.repo.ProductPriceBySKU(ctx, input.Body.StoreSKU)
	if err != nil {
		return nil, huma.Error500InternalServerError("price lookup failed", err)
	}

	targetSKU := input.Body.StoreSKU
	if input.Body.AdoptSKU {
		targetSKU = input.Body.PartNo
	}
	if err := h.repo.UpsertStoreLink(ctx, input.Body.PartNo, targetSKU, price); err != nil {
		return nil, huma.Error500InternalServerError("Failed to create link", err)
	}

	if input.Body.AdoptSKU {
		if _, err := h.repo.AdoptProductSKU(ctx, input.Body.StoreSKU, input.Body.PartNo); err != nil {
			return nil, huma.Error500InternalServerError("Link created but failed to update Product SKU", err)
		}
	}

	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

// --- Mappings ---

type adminAuthInput struct {
	Authorization string `header:"Authorization"`
}

type ListMappingsOutput struct {
	Body struct {
		Mappings []MappingRow `json:"mappings"`
	}
}

func (h *Handlers) ListMappings(ctx context.Context, input *adminAuthInput) (*ListMappingsOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	mappings, err := h.repo.Mappings(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("Failed to fetch mappings", err)
	}
	out := &ListMappingsOutput{}
	out.Body.Mappings = mappings
	return out, nil
}

type SaveMappingInput struct {
	Authorization string `header:"Authorization"`
	Action        string `query:"action" enum:"map,ignore,unignore"`
	Body          struct {
		SupplierSheet     string  `json:"supplier_sheet"`
		SupplierCategory  string  `json:"supplier_category"`
		StoreCategoryID   *string `json:"store_category_id,omitempty"`
		StoreCategoryName *string `json:"store_category_name,omitempty"`
	}
}

func (h *Handlers) SaveMapping(ctx context.Context, input *SaveMappingInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.SupplierSheet == "" || input.Body.SupplierCategory == "" {
		return nil, huma.Error400BadRequest("supplier_sheet and supplier_category are required")
	}
	if err := h.repo.UpsertMappingAction(ctx, input.Action, input.Body.SupplierSheet, input.Body.SupplierCategory, input.Body.StoreCategoryID, input.Body.StoreCategoryName); err != nil {
		return nil, huma.Error500InternalServerError("Failed to save mapping", err)
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

// --- Settings ---

type ListSettingsOutput struct {
	Body []SyncSetting
}

func (h *Handlers) ListSettings(ctx context.Context, input *adminAuthInput) (*ListSettingsOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	settings, err := h.repo.SyncSettings(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("Failed to fetch settings", err)
	}
	return &ListSettingsOutput{Body: settings}, nil
}

// SaveSyncSettingInput is deliberately not named SaveSettingInput — huma
// registers OpenAPI schemas by type name, and internal/store already owns a
// differently-shaped SaveSettingInput. A duplicate name with a different
// shape panics at startup registration (learned from a failed deploy).
type SaveSyncSettingInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
}

func (h *Handlers) SaveSetting(ctx context.Context, input *SaveSyncSettingInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.Key == "" {
		return nil, huma.Error400BadRequest("key is required")
	}
	if err := h.repo.UpsertSyncSetting(ctx, input.Body.Key, input.Body.Value); err != nil {
		return nil, huma.Error500InternalServerError("Failed to update setting", err)
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

// --- Item override ---

type OverrideInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		PartNo            string `json:"part_no"`
		StoreCategoryID   string `json:"store_category_id"`
		StoreCategoryName string `json:"store_category_name"`
	}
}

func (h *Handlers) Override(ctx context.Context, input *OverrideInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.PartNo == "" || input.Body.StoreCategoryID == "" {
		return nil, huma.Error400BadRequest("part_no and store_category_id are required")
	}
	if err := h.repo.SetItemOverride(ctx, input.Body.PartNo, input.Body.StoreCategoryID, input.Body.StoreCategoryName); err != nil {
		return nil, huma.Error500InternalServerError("Failed to update override", err)
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

// --- Catalog browse ---

type CatalogInput struct {
	Authorization string `header:"Authorization"`
	Search        string `query:"search"`
	Brand         string `query:"brand"`
	Page          int    `query:"page" minimum:"1" default:"1"`
	PageSize      int    `query:"page_size" minimum:"1" maximum:"200" default:"50"`
}

type CatalogOutput struct {
	Body struct {
		Products []CatalogProduct `json:"products"`
		Total    int              `json:"total"`
	}
}

func (h *Handlers) Catalog(ctx context.Context, input *CatalogInput) (*CatalogOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	products, total, err := h.repo.CatalogPage(ctx, input.Search, input.Brand, input.Page, input.PageSize)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load supplier catalog", err)
	}
	out := &CatalogOutput{}
	out.Body.Products = products
	out.Body.Total = total
	return out, nil
}

// --- Session history ---

type SessionsOutput struct {
	Body struct {
		Sessions []SyncSession `json:"sessions"`
	}
}

func (h *Handlers) Sessions(ctx context.Context, input *adminAuthInput) (*SessionsOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	sessions, err := h.repo.ListSessions(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load sync sessions", err)
	}
	out := &SessionsOutput{}
	out.Body.Sessions = sessions
	return out, nil
}
