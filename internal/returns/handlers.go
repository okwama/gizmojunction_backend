package returns

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
		OperationID: "create-return",
		Method:      http.MethodPost,
		Path:        "/v1/admin/orders/{id}/returns",
		Summary:     "Record a return/refund against an order — restocks or writes off each line, flags a KRA credit note as required if the order was fiscalized (admin only)",
	}, h.CreateReturn)

	huma.Register(api, huma.Operation{
		OperationID: "list-returns",
		Method:      http.MethodGet,
		Path:        "/v1/admin/returns",
		Summary:     "All returns (admin only)",
	}, h.ListReturns)

	huma.Register(api, huma.Operation{
		OperationID: "list-order-returns",
		Method:      http.MethodGet,
		Path:        "/v1/admin/orders/{id}/returns",
		Summary:     "Returns for one order, plus already-returned quantities per line (admin only)",
	}, h.ListReturnsForOrder)

	huma.Register(api, huma.Operation{
		OperationID: "mark-credit-note-issued",
		Method:      http.MethodPatch,
		Path:        "/v1/admin/returns/{id}/credit-note",
		Summary:     "Record that a KRA credit note has been manually filed for this return (admin only)",
	}, h.MarkCreditNoteIssued)
}

type adminAuthInput struct {
	Authorization string `header:"Authorization"`
}

type successOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

type ReturnItemBody struct {
	OrderItemID string `json:"order_item_id"`
	Quantity    int32  `json:"quantity" minimum:"1"`
	Condition   string `json:"condition" enum:"restock,damaged"`
}

type CreateReturnInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
	Body          struct {
		Items        []ReturnItemBody `json:"items"`
		Reason       string           `json:"reason,omitempty" required:"false"`
		RefundMethod string           `json:"refund_method" enum:"cash,mpesa_manual,card_manual,none"`
		RefundAmount float64          `json:"refund_amount" minimum:"0"`
		// RefundReference is an optional free-text note (e.g. an M-Pesa
		// transaction code for a manually-sent refund) — nothing here
		// triggers a real payment; see the package comment.
		RefundReference string `json:"refund_reference,omitempty" required:"false"`
	}
}

type ReturnOutput struct {
	Body struct {
		ID string `json:"id"`
	}
}

func (h *Handlers) CreateReturn(ctx context.Context, input *CreateReturnInput) (*ReturnOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.Items) == 0 {
		return nil, huma.Error400BadRequest("at least one item is required")
	}

	items := make([]ReturnItemInput, len(input.Body.Items))
	for i, it := range input.Body.Items {
		items[i] = ReturnItemInput{OrderItemID: it.OrderItemID, Quantity: it.Quantity, Condition: it.Condition}
	}

	returnID, err := h.repo.CreateReturn(ctx, NewReturn{
		OrderID:         input.ID,
		Items:           items,
		Reason:          input.Body.Reason,
		RefundMethod:    input.Body.RefundMethod,
		RefundAmount:    input.Body.RefundAmount,
		RefundReference: input.Body.RefundReference,
	})
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}

	out := &ReturnOutput{}
	out.Body.ID = returnID
	return out, nil
}

func (h *Handlers) ListReturns(ctx context.Context, input *adminAuthInput) (*struct{ Body []Return }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	list, err := h.repo.ListReturns(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load returns", err)
	}
	return &struct{ Body []Return }{Body: list}, nil
}

type ListOrderReturnsInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

type ListOrderReturnsOutput struct {
	Body struct {
		Returns            []Return         `json:"returns"`
		ReturnedQuantities map[string]int32 `json:"returned_quantities"`
	}
}

func (h *Handlers) ListReturnsForOrder(ctx context.Context, input *ListOrderReturnsInput) (*ListOrderReturnsOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	list, err := h.repo.ListReturnsForOrder(ctx, input.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load returns", err)
	}
	qtys, err := h.repo.ReturnedQuantities(ctx, input.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load returned quantities", err)
	}

	out := &ListOrderReturnsOutput{}
	out.Body.Returns = list
	out.Body.ReturnedQuantities = qtys
	return out, nil
}

type MarkCreditNoteInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
	Body          struct {
		Reference string `json:"reference"`
	}
}

func (h *Handlers) MarkCreditNoteIssued(ctx context.Context, input *MarkCreditNoteInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if err := h.repo.MarkCreditNoteIssued(ctx, input.ID, input.Body.Reference); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("return not found")
		}
		return nil, huma.Error500InternalServerError("failed to update credit note status", err)
	}

	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}
