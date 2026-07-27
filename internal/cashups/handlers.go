package cashups

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"gizmojunction/backend/internal/auth"
)

type Handlers struct {
	repo    *Repo
	authSvc *auth.Service
}

func Register(api huma.API, repo *Repo, authSvc *auth.Service) {
	h := &Handlers{repo: repo, authSvc: authSvc}

	huma.Register(api, huma.Operation{
		OperationID: "cashup-shift-summary",
		Method:      http.MethodGet,
		Path:        "/v1/admin/cash-ups/shift-summary",
		Summary:     "Preview the caller's current open shift — expected cash by payment method since their last cash-up (admin or cashier)",
	}, h.ShiftSummary)

	huma.Register(api, huma.Operation{
		OperationID: "submit-cashup",
		Method:      http.MethodPost,
		Path:        "/v1/admin/cash-ups",
		Summary:     "Record the caller's counted cash, close their shift, and compute variance (admin or cashier)",
	}, h.SubmitCashUp)

	huma.Register(api, huma.Operation{
		OperationID: "list-cashups",
		Method:      http.MethodGet,
		Path:        "/v1/admin/cash-ups",
		Summary:     "Cash-up history — all cashiers for ADMIN, own only for CASHIER",
	}, h.ListCashUps)
}

type authInput struct {
	Authorization string `header:"Authorization"`
}

func (h *Handlers) ShiftSummary(ctx context.Context, input *authInput) (*struct{ Body Summary }, error) {
	claims, err := h.authSvc.RequireRole(input.Authorization, "ADMIN", "CASHIER")
	if err != nil {
		return nil, err
	}
	summary, err := h.repo.ShiftSummary(ctx, claims.ProfileID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load shift summary", err)
	}
	return &struct{ Body Summary }{Body: summary}, nil
}

type SubmitCashUpInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		CountedCash float64 `json:"counted_cash" minimum:"0"`
		Notes       string  `json:"notes,omitempty" required:"false"`
	}
}

func (h *Handlers) SubmitCashUp(ctx context.Context, input *SubmitCashUpInput) (*struct{ Body CashUp }, error) {
	claims, err := h.authSvc.RequireRole(input.Authorization, "ADMIN", "CASHIER")
	if err != nil {
		return nil, err
	}
	cashUp, err := h.repo.SubmitCashUp(ctx, claims.ProfileID, input.Body.CountedCash, input.Body.Notes)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to submit cash-up", err)
	}
	return &struct{ Body CashUp }{Body: cashUp}, nil
}

type ListCashUpsInput struct {
	Authorization string `header:"Authorization"`
	CashierID     string `query:"cashier_id,omitempty" required:"false"`
}

// ListCashUps forces a CASHIER caller to their own claims.ProfileID
// regardless of any cashier_id query param — enforced server-side, not
// just hidden in the UI.
func (h *Handlers) ListCashUps(ctx context.Context, input *ListCashUpsInput) (*struct{ Body []CashUp }, error) {
	claims, err := h.authSvc.RequireRole(input.Authorization, "ADMIN", "CASHIER")
	if err != nil {
		return nil, err
	}

	var cashierID *string
	if claims.Role == "CASHIER" {
		cashierID = &claims.ProfileID
	} else if input.CashierID != "" {
		cashierID = &input.CashierID
	}

	list, err := h.repo.ListCashUps(ctx, cashierID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load cash-ups", err)
	}
	return &struct{ Body []CashUp }{Body: list}, nil
}
