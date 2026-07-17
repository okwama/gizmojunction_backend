// Package account covers signed-in customers acting on their own data:
// profile updates, saved addresses, and review submission. Everything is
// scoped to the authenticated token's own profile id — the old frontend
// passed user ids from client JS and trusted RLS to catch mismatches.
package account

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gizmojunction/backend/internal/auth"
)

type Handlers struct {
	pool    *pgxpool.Pool
	authSvc *auth.Service
}

func Register(api huma.API, pool *pgxpool.Pool, authSvc *auth.Service) {
	h := &Handlers{pool: pool, authSvc: authSvc}

	huma.Register(api, huma.Operation{
		OperationID: "update-own-profile",
		Method:      http.MethodPatch,
		Path:        "/v1/auth/me",
		Summary:     "Update the signed-in user's own name/phone",
	}, h.UpdateProfile)

	huma.Register(api, huma.Operation{
		OperationID: "list-own-addresses",
		Method:      http.MethodGet,
		Path:        "/v1/me/addresses",
		Summary:     "The signed-in user's saved addresses",
	}, h.ListAddresses)

	huma.Register(api, huma.Operation{
		OperationID: "create-own-address",
		Method:      http.MethodPost,
		Path:        "/v1/me/addresses",
		Summary:     "Add a saved address",
	}, h.CreateAddress)

	huma.Register(api, huma.Operation{
		OperationID: "update-own-address",
		Method:      http.MethodPatch,
		Path:        "/v1/me/addresses/{id}",
		Summary:     "Update one of the signed-in user's addresses",
	}, h.UpdateAddress)

	huma.Register(api, huma.Operation{
		OperationID: "delete-own-address",
		Method:      http.MethodDelete,
		Path:        "/v1/me/addresses/{id}",
		Summary:     "Delete one of the signed-in user's addresses",
	}, h.DeleteAddress)

	huma.Register(api, huma.Operation{
		OperationID: "submit-review",
		Method:      http.MethodPost,
		Path:        "/v1/reviews",
		Summary:     "Submit a product review (anonymous allowed, matching previous behavior)",
	}, h.SubmitReview)
}

// --- Profile ---

type UpdateProfileInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		FullName string `json:"full_name,omitempty"`
		Phone    string `json:"phone,omitempty"`
	}
}

type successOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

func (h *Handlers) UpdateProfile(ctx context.Context, input *UpdateProfileInput) (*successOutput, error) {
	claims, err := h.authSvc.Authenticate(input.Authorization)
	if err != nil {
		return nil, err
	}
	_, err = h.pool.Exec(ctx, `
		UPDATE profiles SET
			full_name = COALESCE(NULLIF($2, ''), full_name),
			phone = COALESCE(NULLIF($3, ''), phone),
			updated_at = now()
		WHERE id = $1`, claims.ProfileID, input.Body.FullName, input.Body.Phone)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to update profile", err)
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

// --- Addresses ---

type Address struct {
	ID            string    `db:"id" json:"id"`
	IsDefault     bool      `db:"is_default" json:"is_default"`
	AddressType   *string   `db:"address_type" json:"address_type,omitempty"`
	FullName      string    `db:"full_name" json:"full_name"`
	Phone         *string   `db:"phone" json:"phone,omitempty"`
	StreetAddress string    `db:"street_address" json:"street_address"`
	Apartment     *string   `db:"apartment" json:"apartment,omitempty"`
	City          string    `db:"city" json:"city"`
	County        *string   `db:"county" json:"county,omitempty"`
	PostalCode    *string   `db:"postal_code" json:"postal_code,omitempty"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

const addressColumns = `id::text, COALESCE(is_default, false) AS is_default, address_type, full_name, phone, street_address, apartment, city, county, postal_code, created_at`

type authInput struct {
	Authorization string `header:"Authorization"`
}

func (h *Handlers) ListAddresses(ctx context.Context, input *authInput) (*struct{ Body []Address }, error) {
	claims, err := h.authSvc.Authenticate(input.Authorization)
	if err != nil {
		return nil, err
	}
	rows, err := h.pool.Query(ctx, `
		SELECT `+addressColumns+` FROM addresses
		WHERE user_id = $1
		ORDER BY is_default DESC, created_at DESC`, claims.ProfileID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load addresses", err)
	}
	addresses, err := pgx.CollectRows(rows, pgx.RowToStructByName[Address])
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load addresses", err)
	}
	return &struct{ Body []Address }{Body: addresses}, nil
}

type addressBody struct {
	FullName      string `json:"full_name"`
	Phone         string `json:"phone,omitempty"`
	StreetAddress string `json:"street_address"`
	Apartment     string `json:"apartment,omitempty"`
	City          string `json:"city"`
	County        string `json:"county,omitempty"`
	PostalCode    string `json:"postal_code,omitempty"`
	AddressType   string `json:"address_type,omitempty" enum:"SHIPPING,BILLING"`
	IsDefault     bool   `json:"is_default,omitempty"`
}

type CreateAddressInput struct {
	Authorization string `header:"Authorization"`
	Body          addressBody
}

func (h *Handlers) CreateAddress(ctx context.Context, input *CreateAddressInput) (*successOutput, error) {
	claims, err := h.authSvc.Authenticate(input.Authorization)
	if err != nil {
		return nil, err
	}
	if input.Body.FullName == "" || input.Body.StreetAddress == "" || input.Body.City == "" {
		return nil, huma.Error400BadRequest("full_name, street_address and city are required")
	}
	addressType := input.Body.AddressType
	if addressType == "" {
		addressType = "SHIPPING"
	}
	_, err = h.pool.Exec(ctx, `
		INSERT INTO addresses (user_id, full_name, phone, street_address, apartment, city, county, postal_code, address_type, is_default)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		claims.ProfileID, input.Body.FullName, nullIfEmpty(input.Body.Phone), input.Body.StreetAddress,
		nullIfEmpty(input.Body.Apartment), input.Body.City, nullIfEmpty(input.Body.County),
		nullIfEmpty(input.Body.PostalCode), addressType, input.Body.IsDefault)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to save address", err)
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

type UpdateAddressInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
	Body          addressBody
}

func (h *Handlers) UpdateAddress(ctx context.Context, input *UpdateAddressInput) (*successOutput, error) {
	claims, err := h.authSvc.Authenticate(input.Authorization)
	if err != nil {
		return nil, err
	}
	tag, err := h.pool.Exec(ctx, `
		UPDATE addresses SET
			full_name = $3, phone = $4, street_address = $5, apartment = $6,
			city = $7, county = $8, postal_code = $9, address_type = $10, is_default = $11
		WHERE id = $1 AND user_id = $2`,
		input.ID, claims.ProfileID, input.Body.FullName, nullIfEmpty(input.Body.Phone),
		input.Body.StreetAddress, nullIfEmpty(input.Body.Apartment), input.Body.City,
		nullIfEmpty(input.Body.County), nullIfEmpty(input.Body.PostalCode),
		defaultString(input.Body.AddressType, "SHIPPING"), input.Body.IsDefault)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to update address", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, huma.Error404NotFound("address not found")
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

type DeleteAddressInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
}

func (h *Handlers) DeleteAddress(ctx context.Context, input *DeleteAddressInput) (*successOutput, error) {
	claims, err := h.authSvc.Authenticate(input.Authorization)
	if err != nil {
		return nil, err
	}
	tag, err := h.pool.Exec(ctx, `DELETE FROM addresses WHERE id = $1 AND user_id = $2`, input.ID, claims.ProfileID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to delete address", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, huma.Error404NotFound("address not found")
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

// --- Reviews ---

type SubmitReviewInput struct {
	Authorization string `header:"Authorization,omitempty"`
	Body          struct {
		ProductID string `json:"product_id"`
		Rating    int    `json:"rating" minimum:"1" maximum:"5"`
		Comment   string `json:"comment"`
		UserName  string `json:"user_name"`
	}
}

// SubmitReview stays open to anonymous visitors like the old direct insert
// did, but the reviewer identity now comes from the verified token when one
// is present, instead of a client-supplied user_id.
func (h *Handlers) SubmitReview(ctx context.Context, input *SubmitReviewInput) (*successOutput, error) {
	if input.Body.ProductID == "" || input.Body.Comment == "" || input.Body.UserName == "" {
		return nil, huma.Error400BadRequest("product_id, comment and user_name are required")
	}

	var customerID *string
	if claims, err := h.authSvc.Authenticate(input.Authorization); err == nil {
		customerID = &claims.ProfileID
	}

	_, err := h.pool.Exec(ctx, `
		INSERT INTO reviews (product_id, customer_id, author_name, rating, comment)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
		input.Body.ProductID, customerID, input.Body.UserName, input.Body.Rating, input.Body.Comment)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to submit review", err)
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func defaultString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
