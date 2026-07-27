package shifts

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
		OperationID: "list-active-shifts",
		Method:      http.MethodGet,
		Path:        "/v1/admin/shifts/active",
		Summary:     "Cashiers currently logged in and on shift (admin only)",
	}, h.ListActive)
}

type authInput struct {
	Authorization string `header:"Authorization"`
}

func (h *Handlers) ListActive(ctx context.Context, input *authInput) (*struct{ Body []ActiveShift }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	list, err := h.repo.ListActive(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load active shifts", err)
	}
	return &struct{ Body []ActiveShift }{Body: list}, nil
}
