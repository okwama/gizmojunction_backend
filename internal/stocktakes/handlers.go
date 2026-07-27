package stocktakes

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"gizmojunction/backend/internal/auth"
)

type Handlers struct {
	repo    *Repo
	authSvc *auth.Service
}

func Register(api huma.API, repo *Repo, authSvc *auth.Service) {
	h := &Handlers{repo: repo, authSvc: authSvc}

	huma.Register(api, huma.Operation{
		OperationID: "start-stock-take",
		Method:      http.MethodPost,
		Path:        "/v1/admin/stock-takes",
		Summary:     "Start a stock take, snapshotting expected quantities for a category or the whole catalog (admin only)",
	}, h.StartStockTake)

	huma.Register(api, huma.Operation{
		OperationID: "list-stock-takes",
		Method:      http.MethodGet,
		Path:        "/v1/admin/stock-takes",
		Summary:     "All stock takes (admin only)",
	}, h.ListStockTakes)

	huma.Register(api, huma.Operation{
		OperationID: "get-stock-take",
		Method:      http.MethodGet,
		Path:        "/v1/admin/stock-takes/{id}",
		Summary:     "One stock take with items, expected/counted quantities and variance (admin only)",
	}, h.GetStockTake)

	huma.Register(api, huma.Operation{
		OperationID: "submit-stock-take-count",
		Method:      http.MethodPost,
		Path:        "/v1/admin/stock-takes/{id}/submit",
		Summary:     "Record counted quantities, adjust stock, and complete the stock take (admin only)",
	}, h.SubmitCount)
}

type adminAuthInput struct {
	Authorization string `header:"Authorization"`
}

type successOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

type StartStockTakeInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		CategoryID string `json:"category_id,omitempty" required:"false"`
	}
}

type StockTakeIDOutput struct {
	Body struct {
		ID string `json:"id"`
	}
}

func (h *Handlers) StartStockTake(ctx context.Context, input *StartStockTakeInput) (*StockTakeIDOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}

	var categoryID *string
	if input.Body.CategoryID != "" {
		categoryID = &input.Body.CategoryID
	}

	id, err := h.repo.StartStockTake(ctx, categoryID)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	out := &StockTakeIDOutput{}
	out.Body.ID = id
	return out, nil
}

func (h *Handlers) ListStockTakes(ctx context.Context, input *adminAuthInput) (*struct{ Body []StockTake }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	list, err := h.repo.ListStockTakes(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load stock takes", err)
	}
	return &struct{ Body []StockTake }{Body: list}, nil
}

type stockTakeIDInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

func (h *Handlers) GetStockTake(ctx context.Context, input *stockTakeIDInput) (*struct{ Body StockTakeDetail }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	st, err := h.repo.GetStockTake(ctx, input.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("stock take not found")
		}
		return nil, huma.Error500InternalServerError("failed to load stock take", err)
	}
	return &struct{ Body StockTakeDetail }{Body: st}, nil
}

type CountItemBody struct {
	StockTakeItemID string `json:"stock_take_item_id"`
	CountedQuantity int32  `json:"counted_quantity" minimum:"0"`
	Reason          string `json:"reason,omitempty" required:"false"`
}

type SubmitCountInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
	Body          struct {
		Counts []CountItemBody `json:"counts"`
	}
}

func (h *Handlers) SubmitCount(ctx context.Context, input *SubmitCountInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.Counts) == 0 {
		return nil, huma.Error400BadRequest("at least one counted item is required")
	}

	counts := make([]CountInput, len(input.Body.Counts))
	for i, c := range input.Body.Counts {
		counts[i] = CountInput{StockTakeItemID: c.StockTakeItemID, CountedQuantity: c.CountedQuantity, Reason: c.Reason}
	}

	if err := h.repo.SubmitCount(ctx, input.ID, counts); err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}
