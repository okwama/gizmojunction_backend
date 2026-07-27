package quotes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"gizmojunction/backend/internal/auth"
	"gizmojunction/backend/internal/jobs"
	"gizmojunction/backend/internal/storage"
)

type Handlers struct {
	repo             *Repo
	authSvc          *auth.Service
	store            *storage.Client // nil when R2 isn't configured — PDF/email disabled
	email            *jobs.EmailSender
	backendPublicURL string
}

func Register(api huma.API, repo *Repo, authSvc *auth.Service, store *storage.Client, email *jobs.EmailSender, backendPublicURL string) {
	h := &Handlers{repo: repo, authSvc: authSvc, store: store, email: email, backendPublicURL: backendPublicURL}

	huma.Register(api, huma.Operation{
		OperationID: "create-quote",
		Method:      http.MethodPost,
		Path:        "/v1/admin/quotes",
		Summary:     "Create a quote with line items (admin only)",
	}, h.CreateQuote)

	huma.Register(api, huma.Operation{
		OperationID: "list-quotes",
		Method:      http.MethodGet,
		Path:        "/v1/admin/quotes",
		Summary:     "All quotes (admin only)",
	}, h.ListQuotes)

	huma.Register(api, huma.Operation{
		OperationID: "get-quote",
		Method:      http.MethodGet,
		Path:        "/v1/admin/quotes/{id}",
		Summary:     "One quote with items (admin only)",
	}, h.GetQuote)

	huma.Register(api, huma.Operation{
		OperationID: "update-quote-status",
		Method:      http.MethodPatch,
		Path:        "/v1/admin/quotes/{id}",
		Summary:     "Update a quote's status: draft|sent|accepted|expired (admin only)",
	}, h.UpdateQuoteStatus)

	huma.Register(api, huma.Operation{
		OperationID: "generate-quote-pdf",
		Method:      http.MethodPost,
		Path:        "/v1/admin/quotes/{id}/pdf",
		Summary:     "Generate and store the quote's PDF (admin only)",
	}, h.GeneratePDF)

	huma.Register(api, huma.Operation{
		OperationID: "email-quote",
		Method:      http.MethodPost,
		Path:        "/v1/admin/quotes/{id}/email",
		Summary:     "Email the quote PDF to the customer (admin only)",
	}, h.EmailQuote)

	huma.Register(api, huma.Operation{
		OperationID: "convert-quote-to-order",
		Method:      http.MethodPost,
		Path:        "/v1/admin/quotes/{id}/convert",
		Summary:     "Convert a sent/accepted quote into a real order at its quoted prices (admin only)",
	}, h.ConvertToOrder)
}

type adminAuthInput struct {
	Authorization string `header:"Authorization"`
}

type quoteIDInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

type QuoteItemBody struct {
	ProductID string  `json:"product_id,omitempty" required:"false"`
	Name      string  `json:"name"`
	SKU       string  `json:"sku,omitempty" required:"false"`
	Quantity  int32   `json:"quantity" minimum:"1"`
	UnitPrice float64 `json:"unit_price" minimum:"0"`
	TaxClass  string  `json:"tax_class,omitempty" required:"false"`
}

type CreateQuoteInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		CustomerName  string `json:"customer_name"`
		CustomerEmail string `json:"customer_email,omitempty" required:"false"`
		CustomerPhone string `json:"customer_phone,omitempty" required:"false"`
		Notes         string `json:"notes,omitempty" required:"false"`
		// ValidUntil is "YYYY-MM-DD"; omitted means no expiry set.
		ValidUntil string          `json:"valid_until,omitempty" required:"false"`
		Items      []QuoteItemBody `json:"items"`
	}
}

type QuoteIDOutput struct {
	Body struct {
		ID string `json:"id"`
	}
}

func (h *Handlers) CreateQuote(ctx context.Context, input *CreateQuoteInput) (*QuoteIDOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.Items) == 0 {
		return nil, huma.Error400BadRequest("at least one item is required")
	}

	var validUntil *time.Time
	if input.Body.ValidUntil != "" {
		t, err := time.Parse("2006-01-02", input.Body.ValidUntil)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid valid_until date, expected YYYY-MM-DD")
		}
		validUntil = &t
	}

	items := make([]QuoteItemInput, len(input.Body.Items))
	for i, it := range input.Body.Items {
		var productID *string
		if it.ProductID != "" {
			productID = &it.ProductID
		}
		items[i] = QuoteItemInput{ProductID: productID, Name: it.Name, SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice, TaxClass: it.TaxClass}
	}

	id, err := h.repo.CreateQuote(ctx, NewQuote{
		CustomerName:  input.Body.CustomerName,
		CustomerEmail: input.Body.CustomerEmail,
		CustomerPhone: input.Body.CustomerPhone,
		Notes:         input.Body.Notes,
		ValidUntil:    validUntil,
		Items:         items,
	})
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}

	out := &QuoteIDOutput{}
	out.Body.ID = id
	return out, nil
}

func (h *Handlers) ListQuotes(ctx context.Context, input *adminAuthInput) (*struct{ Body []Quote }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	list, err := h.repo.ListQuotes(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load quotes", err)
	}
	return &struct{ Body []Quote }{Body: list}, nil
}

func (h *Handlers) GetQuote(ctx context.Context, input *quoteIDInput) (*struct{ Body Quote }, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	q, err := h.repo.GetQuote(ctx, input.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("quote not found")
		}
		return nil, huma.Error500InternalServerError("failed to load quote", err)
	}
	return &struct{ Body Quote }{Body: *q}, nil
}

type UpdateQuoteStatusInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
	Body          struct {
		Status string `json:"status" enum:"draft,sent,accepted,expired"`
	}
}

type successOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

func (h *Handlers) UpdateQuoteStatus(ctx context.Context, input *UpdateQuoteStatusInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if err := h.repo.UpdateQuoteStatus(ctx, input.ID, input.Body.Status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, huma.Error404NotFound("quote not found (or already converted)")
		}
		return nil, huma.Error400BadRequest(err.Error())
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

// generateAndStorePDF is shared by GeneratePDF and EmailQuote so the quote
// email always attaches a freshly-rendered PDF rather than requiring the
// admin to generate one first.
func (h *Handlers) generateAndStorePDF(ctx context.Context, id string) (string, *Quote, error) {
	if h.store == nil {
		return "", nil, huma.Error503ServiceUnavailable("document storage (R2) is not configured")
	}
	q, err := h.repo.GetQuote(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, huma.Error404NotFound("quote not found")
		}
		return "", nil, huma.Error500InternalServerError("failed to load quote", err)
	}

	pdf, err := renderQuote(*q)
	if err != nil {
		return "", nil, huma.Error500InternalServerError("failed to generate quote PDF", err)
	}

	path := "documents/quotes/QUOTE-" + q.ID[:8] + ".pdf"
	if err := h.store.Upload(ctx, path, pdf, "application/pdf"); err != nil {
		return "", nil, huma.Error500InternalServerError("failed to store quote PDF", err)
	}
	return path, q, nil
}

type GeneratePDFOutput struct {
	Body struct {
		Path string `json:"path"`
	}
}

func (h *Handlers) GeneratePDF(ctx context.Context, input *quoteIDInput) (*GeneratePDFOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	path, _, err := h.generateAndStorePDF(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	out := &GeneratePDFOutput{}
	out.Body.Path = path
	return out, nil
}

// EmailQuote is a direct, synchronous EmailSender.Send call rather than a
// River job — this is a manual, low-volume admin action (unlike automatic
// system emails like order confirmations), so a simple success/failure
// returned straight to the click is simpler than standing up a worker,
// job-kind registration, and retry policy for it.
func (h *Handlers) EmailQuote(ctx context.Context, input *quoteIDInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if h.email == nil {
		return nil, huma.Error503ServiceUnavailable("email is not configured")
	}

	path, q, err := h.generateAndStorePDF(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	if q.CustomerEmail == nil || *q.CustomerEmail == "" {
		return nil, huma.Error400BadRequest("this quote has no customer email on file")
	}

	docURL := strings.TrimRight(h.backendPublicURL, "/") + "/v1/documents/" + path
	html := fmt.Sprintf(
		"<p>Hi %s,</p><p>Please find your quotation below.</p><p><a href=\"%s\">Download Quote (PDF)</a></p><p>Total: KES %.2f. Valid until %s.</p>",
		q.CustomerName, docURL, q.TotalAmount, validUntilLabel(q),
	)

	if err := h.email.Send(ctx, jobs.EmailPayload{
		From:    "Gizmo Junction <sales@notify.gizmojunction.com>",
		To:      []string{*q.CustomerEmail},
		Subject: "Your GizmoJunction Quotation",
		HTML:    html,
	}); err != nil {
		return nil, huma.Error502BadGateway("failed to send email: " + err.Error())
	}

	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

func validUntilLabel(q *Quote) string {
	if q.ValidUntil == nil {
		return "further notice"
	}
	return q.ValidUntil.Format("02 Jan 2006")
}

type ConvertQuoteOutput struct {
	Body struct {
		OrderID string `json:"order_id"`
	}
}

func (h *Handlers) ConvertToOrder(ctx context.Context, input *quoteIDInput) (*ConvertQuoteOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	orderID, err := h.repo.ConvertToOrder(ctx, input.ID)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	out := &ConvertQuoteOutput{}
	out.Body.OrderID = orderID
	return out, nil
}
