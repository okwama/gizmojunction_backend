// Package leads covers the storefront's Contact page and Register-for-the-
// Club forms: two public, unauthenticated endpoints that record a
// submission and notify the store's admin inbox. Both previously had no
// backend at all — the frontend just flipped a local "submitted" flag with
// no network call. Modeled directly on internal/newsletter (same
// insert-only, no-RLS pattern), with an added best-effort admin-notification
// email reusing the existing jobs.EmailSender.
package leads

import (
	"context"
	"fmt"
	"html"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"gizmojunction/backend/internal/jobs"
)

func escapeHTML(s string) string {
	return html.EscapeString(s)
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) SaveContactMessage(ctx context.Context, name, email, subject, message string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO contact_messages (name, email, subject, message) VALUES ($1, $2, $3, $4)`,
		name, email, subject, message)
	return err
}

func (r *Repo) SaveClubRegistration(ctx context.Context, name, email, phone, location string, interests []string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO club_registrations (name, email, phone, location, interests) VALUES ($1, $2, $3, $4, $5)`,
		name, email, phone, location, interests)
	return err
}

// --- Handlers ---

type Handlers struct {
	repo       *Repo
	email      *jobs.EmailSender
	adminEmail string
}

func Register(api huma.API, repo *Repo, email *jobs.EmailSender, adminEmail string) {
	h := &Handlers{repo: repo, email: email, adminEmail: adminEmail}

	huma.Register(api, huma.Operation{
		OperationID: "contact-submit",
		Method:      http.MethodPost,
		Path:        "/v1/contact",
		Summary:     "Submit the storefront Contact Us form",
	}, h.SubmitContact)

	huma.Register(api, huma.Operation{
		OperationID: "club-register",
		Method:      http.MethodPost,
		Path:        "/v1/club/register",
		Summary:     "Register for the GizmoJunction Club",
	}, h.SubmitClubRegistration)
}

type ContactInput struct {
	Body struct {
		Name    string `json:"name" doc:"Full name"`
		Email   string `json:"email" format:"email" doc:"Reply-to email address"`
		Subject string `json:"subject,omitempty" doc:"Message subject"`
		Message string `json:"message" doc:"Message body"`
	}
}

type ContactOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

func (h *Handlers) SubmitContact(ctx context.Context, input *ContactInput) (*ContactOutput, error) {
	if input.Body.Name == "" || input.Body.Email == "" || input.Body.Message == "" {
		return nil, huma.Error400BadRequest("name, email, and message are required")
	}
	if err := h.repo.SaveContactMessage(ctx, input.Body.Name, input.Body.Email, input.Body.Subject, input.Body.Message); err != nil {
		return nil, huma.Error500InternalServerError("failed to save message", err)
	}

	if h.email != nil && h.adminEmail != "" {
		subject := input.Body.Subject
		if subject == "" {
			subject = "New contact form message"
		}
		_ = h.email.Send(ctx, jobs.EmailPayload{
			From:    "Gizmo Junction <noreply@notify.gizmojunction.com>",
			To:      []string{h.adminEmail},
			Subject: fmt.Sprintf("[Contact] %s", subject),
			HTML: fmt.Sprintf(
				"<p><strong>From:</strong> %s &lt;%s&gt;</p><p>%s</p>",
				escapeHTML(input.Body.Name), escapeHTML(input.Body.Email), escapeHTML(input.Body.Message),
			),
		})
	}

	out := &ContactOutput{}
	out.Body.Success = true
	return out, nil
}

type ClubRegisterInput struct {
	Body struct {
		Name      string   `json:"name" doc:"Full name"`
		Email     string   `json:"email" format:"email" doc:"Email address"`
		Phone     string   `json:"phone,omitempty" doc:"Phone number"`
		Location  string   `json:"location,omitempty" doc:"City / area"`
		Interests []string `json:"interests,omitempty" doc:"Selected interest tags"`
	}
}

type ClubRegisterOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

func (h *Handlers) SubmitClubRegistration(ctx context.Context, input *ClubRegisterInput) (*ClubRegisterOutput, error) {
	if input.Body.Name == "" || input.Body.Email == "" {
		return nil, huma.Error400BadRequest("name and email are required")
	}
	if err := h.repo.SaveClubRegistration(ctx, input.Body.Name, input.Body.Email, input.Body.Phone, input.Body.Location, input.Body.Interests); err != nil {
		return nil, huma.Error500InternalServerError("failed to save registration", err)
	}

	if h.email != nil {
		if h.adminEmail != "" {
			_ = h.email.Send(ctx, jobs.EmailPayload{
				From:    "Gizmo Junction <noreply@notify.gizmojunction.com>",
				To:      []string{h.adminEmail},
				Subject: "[Club] New registration",
				HTML: fmt.Sprintf(
					"<p><strong>%s</strong> &lt;%s&gt;</p><p>Phone: %s</p><p>Location: %s</p>",
					escapeHTML(input.Body.Name), escapeHTML(input.Body.Email),
					escapeHTML(input.Body.Phone), escapeHTML(input.Body.Location),
				),
			})
		}
		// Confirmation to the registrant themselves — the frontend success
		// screen tells them to expect this, so it needs to actually happen.
		_ = h.email.Send(ctx, jobs.EmailPayload{
			From:    "Gizmo Junction <noreply@notify.gizmojunction.com>",
			To:      []string{input.Body.Email},
			Subject: "Welcome to the GizmoJunction Club",
			HTML: fmt.Sprintf(
				"<p>Hi %s,</p><p>Thanks for registering for the GizmoJunction Club. We'll be in touch with your welcome guide and upcoming event schedule shortly.</p>",
				escapeHTML(input.Body.Name),
			),
		})
	}

	out := &ClubRegisterOutput{}
	out.Body.Success = true
	return out, nil
}
