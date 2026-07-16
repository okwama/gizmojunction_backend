package erp

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"gizmojunction/backend/internal/auth"
	"gizmojunction/backend/internal/storage"
)

// Handlers covers the admin ERP page: inventory overview, supplier
// registration, purchase orders, and LPO PDF generation (previously the
// generate-lpo Deno Edge Function). Everything is gated by
// RequireRole(ADMIN).
type Handlers struct {
	repo    *Repo
	authSvc *auth.Service
	store   *storage.Client // nil when R2 isn't configured
}

func Register(api huma.API, repo *Repo, authSvc *auth.Service, store *storage.Client) {
	h := &Handlers{repo: repo, authSvc: authSvc, store: store}

	huma.Register(api, huma.Operation{
		OperationID: "admin-erp-overview",
		Method:      http.MethodGet,
		Path:        "/v1/admin/erp/overview",
		Summary:     "Inventory stats, recent activity, suppliers and purchase orders in one call (admin only)",
	}, h.GetOverview)

	huma.Register(api, huma.Operation{
		OperationID: "admin-create-supplier",
		Method:      http.MethodPost,
		Path:        "/v1/admin/suppliers",
		Summary:     "Register a supplier (admin only)",
	}, h.CreateSupplier)

	huma.Register(api, huma.Operation{
		OperationID: "admin-create-purchase-order",
		Method:      http.MethodPost,
		Path:        "/v1/admin/purchase-orders",
		Summary:     "Create a purchase order (admin only)",
	}, h.CreatePurchaseOrder)

	huma.Register(api, huma.Operation{
		OperationID: "admin-generate-lpo",
		Method:      http.MethodPost,
		Path:        "/v1/admin/purchase-orders/{id}/lpo",
		Summary:     "Generate a purchase order's LPO PDF and store it (admin only)",
	}, h.GenerateLPO)
}

type adminAuthInput struct {
	Authorization string `header:"Authorization"`
}

func (h *Handlers) GetOverview(ctx context.Context, input *adminAuthInput) (*struct{ Body Overview }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	overview, err := h.repo.Overview(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load ERP overview", err)
	}
	return &struct{ Body Overview }{Body: overview}, nil
}

type CreateSupplierInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Name    string `json:"name"`
		Contact string `json:"contact,omitempty"`
		Email   string `json:"email,omitempty"`
		Terms   string `json:"terms,omitempty"`
	}
}

func (h *Handlers) CreateSupplier(ctx context.Context, input *CreateSupplierInput) (*struct{ Body Supplier }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.Name == "" {
		return nil, huma.Error400BadRequest("name is required")
	}
	supplier, err := h.repo.CreateSupplier(ctx, input.Body.Name, input.Body.Contact, input.Body.Email, input.Body.Terms)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to create supplier", err)
	}
	return &struct{ Body Supplier }{Body: supplier}, nil
}

type CreatePurchaseOrderInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		SupplierID string `json:"supplier_id"`
		Notes      string `json:"notes,omitempty"`
	}
}

type CreatePurchaseOrderOutput struct {
	Body struct {
		ID string `json:"id"`
	}
}

func (h *Handlers) CreatePurchaseOrder(ctx context.Context, input *CreatePurchaseOrderInput) (*CreatePurchaseOrderOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.SupplierID == "" {
		return nil, huma.Error400BadRequest("supplier_id is required")
	}
	id, err := h.repo.CreatePurchaseOrder(ctx, input.Body.SupplierID, input.Body.Notes)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to create purchase order", err)
	}
	out := &CreatePurchaseOrderOutput{}
	out.Body.ID = id
	return out, nil
}

type GenerateLPOInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

type GenerateLPOOutput struct {
	Body struct {
		Path string `json:"path"`
	}
}

func (h *Handlers) GenerateLPO(ctx context.Context, input *GenerateLPOInput) (*GenerateLPOOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if h.store == nil {
		return nil, huma.Error503ServiceUnavailable("document storage (R2) is not configured")
	}

	po, err := h.repo.PurchaseOrderForLPO(ctx, input.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("purchase order not found")
		}
		return nil, huma.Error500InternalServerError("failed to load purchase order", err)
	}

	pdf, err := renderLPO(po)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to generate LPO PDF", err)
	}

	path := "documents/lpos/LPO-" + po.ID[:8] + ".pdf"
	if err := h.store.Upload(ctx, path, pdf, "application/pdf"); err != nil {
		return nil, huma.Error500InternalServerError("failed to store LPO PDF", err)
	}

	out := &GenerateLPOOutput{}
	out.Body.Path = path
	return out, nil
}
