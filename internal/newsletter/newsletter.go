// Package newsletter covers the storefront footer's "Subscribe for
// Exclusive Offers" form: a single public, unauthenticated endpoint that
// records an email address. No admin/read side yet — nothing consumes the
// list yet, so there's nothing to expose or protect beyond the insert
// itself.
package newsletter

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// Subscribe is idempotent — re-submitting an already-subscribed email is a
// normal, expected user action (they forgot they'd already signed up), not
// an error.
func (r *Repo) Subscribe(ctx context.Context, email string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO newsletter_subscribers (email) VALUES ($1)
		ON CONFLICT (email) DO NOTHING`, email)
	return err
}

// --- Handlers ---

type Handlers struct {
	repo *Repo
}

func Register(api huma.API, repo *Repo) {
	h := &Handlers{repo: repo}

	huma.Register(api, huma.Operation{
		OperationID: "newsletter-subscribe",
		Method:      http.MethodPost,
		Path:        "/v1/newsletter/subscribe",
		Summary:     "Subscribe an email address to the storefront newsletter",
	}, h.Subscribe)
}

type NewsletterSubscribeInput struct {
	Body struct {
		Email string `json:"email" format:"email" doc:"Email address to subscribe"`
	}
}

type NewsletterSubscribeOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

func (h *Handlers) Subscribe(ctx context.Context, input *NewsletterSubscribeInput) (*NewsletterSubscribeOutput, error) {
	if input.Body.Email == "" {
		return nil, huma.Error400BadRequest("email is required")
	}
	if err := h.repo.Subscribe(ctx, input.Body.Email); err != nil {
		return nil, huma.Error500InternalServerError("failed to save subscription", err)
	}
	out := &NewsletterSubscribeOutput{}
	out.Body.Success = true
	return out, nil
}
