package labels

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"gizmojunction/backend/internal/auth"
	"gizmojunction/backend/internal/storage"
)

type Handlers struct {
	repo    *Repo
	authSvc *auth.Service
	store   *storage.Client
}

func Register(api huma.API, repo *Repo, authSvc *auth.Service, store *storage.Client) {
	h := &Handlers{repo: repo, authSvc: authSvc, store: store}

	huma.Register(api, huma.Operation{
		OperationID: "generate-shelf-labels",
		Method:      http.MethodPost,
		Path:        "/v1/admin/labels/pdf",
		Summary:     "Generate a printable sheet of barcode + name + price shelf labels (admin or cashier)",
	}, h.GenerateLabels)
}

type LabelItemBody struct {
	ProductID string `json:"product_id"`
	Copies    int32  `json:"copies" minimum:"1"`
}

type GenerateLabelsInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Items []LabelItemBody `json:"items"`
	}
}

type GenerateLabelsOutput struct {
	Body struct {
		Path string `json:"path"`
	}
}

// GenerateLabels also allows CASHIER — GRN receiving is ADMIN-only, but a
// cashier restocking shelves from a delivery might reasonably need to print
// labels too, and this endpoint only reads product name/price/barcode
// (public storefront information), never mutates anything.
func (h *Handlers) GenerateLabels(ctx context.Context, input *GenerateLabelsInput) (*GenerateLabelsOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN", "CASHIER"); err != nil {
		return nil, err
	}
	if len(input.Body.Items) == 0 {
		return nil, huma.Error400BadRequest("at least one item is required")
	}
	if h.store == nil {
		return nil, huma.Error503ServiceUnavailable("document storage (R2) is not configured")
	}

	ids := make([]string, len(input.Body.Items))
	copiesByID := make(map[string]int32, len(input.Body.Items))
	for i, it := range input.Body.Items {
		ids[i] = it.ProductID
		copiesByID[it.ProductID] = it.Copies
	}

	products, err := h.repo.ProductsByIDs(ctx, ids)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load products", err)
	}
	if len(products) == 0 {
		return nil, huma.Error400BadRequest("no matching products found")
	}

	lines := make([]LabelLine, len(products))
	for i, p := range products {
		code := p.SKU
		if p.Barcode != nil && *p.Barcode != "" {
			code = *p.Barcode
		}
		lines[i] = LabelLine{Name: p.Name, Price: p.EffectivePrice(), Code: code, Copies: copiesByID[p.ID]}
	}

	pdf, err := renderLabels(lines)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to generate labels", err)
	}

	path := "documents/labels/LABELS-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".pdf"
	if err := h.store.Upload(ctx, path, pdf, "application/pdf"); err != nil {
		return nil, huma.Error500InternalServerError("failed to store labels PDF", err)
	}

	out := &GenerateLabelsOutput{}
	out.Body.Path = path
	return out, nil
}
