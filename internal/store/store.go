// Package store covers storefront configuration: store_settings key/value
// documents (site logo, socials, etc.) and shipping_zones. Settings reads
// are public — the old Supabase RLS policy was "Public can view store
// settings", so this exposes nothing new.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gizmojunction/backend/internal/auth"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) Setting(ctx context.Context, key string) (json.RawMessage, error) {
	var value json.RawMessage
	err := r.pool.QueryRow(ctx, `SELECT value FROM store_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return value, err
}

func (r *Repo) UpsertSetting(ctx context.Context, key string, value json.RawMessage) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO store_settings (key, value, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}

type ShippingZone struct {
	ID          string    `db:"id" json:"id"`
	Name        string    `db:"name" json:"name"`
	Counties    []string  `db:"counties" json:"counties"`
	StandardFee float64   `db:"standard_fee" json:"standard_fee"`
	ExpressFee  float64   `db:"express_fee" json:"express_fee"`
	SortOrder   int32     `db:"sort_order" json:"sort_order"`
	IsActive    bool      `db:"is_active" json:"is_active"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

const zoneColumns = `id::text, name, counties, standard_fee::float8, express_fee::float8, COALESCE(sort_order, 0) AS sort_order, COALESCE(is_active, true) AS is_active, COALESCE(updated_at, created_at) AS updated_at`

func (r *Repo) ShippingZones(ctx context.Context, activeOnly bool) ([]ShippingZone, error) {
	query := `SELECT ` + zoneColumns + ` FROM shipping_zones`
	if activeOnly {
		query += ` WHERE is_active = true`
	}
	query += ` ORDER BY sort_order ASC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ShippingZone])
}

func (r *Repo) UpdateShippingZone(ctx context.Context, id string, name *string, standardFee, expressFee *float64, isActive *bool) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE shipping_zones SET
			name = COALESCE($2, name),
			standard_fee = COALESCE($3, standard_fee),
			express_fee = COALESCE($4, express_fee),
			is_active = COALESCE($5, is_active),
			updated_at = now()
		WHERE id = $1`, id, name, standardFee, expressFee, isActive)
	return err
}

// --- Handlers ---

type Handlers struct {
	repo    *Repo
	authSvc *auth.Service
}

func Register(api huma.API, repo *Repo, authSvc *auth.Service) {
	h := &Handlers{repo: repo, authSvc: authSvc}

	huma.Register(api, huma.Operation{
		OperationID: "get-setting",
		Method:      http.MethodGet,
		Path:        "/v1/settings/{key}",
		Summary:     "Read a public store settings document",
	}, h.GetSetting)

	huma.Register(api, huma.Operation{
		OperationID: "admin-save-setting",
		Method:      http.MethodPut,
		Path:        "/v1/admin/settings/{key}",
		Summary:     "Upsert a store settings document (admin only)",
	}, h.SaveSetting)

	huma.Register(api, huma.Operation{
		OperationID: "list-shipping-zones",
		Method:      http.MethodGet,
		Path:        "/v1/shipping-zones",
		Summary:     "Active shipping zones for checkout fee calculation",
	}, h.PublicZones)

	huma.Register(api, huma.Operation{
		OperationID: "admin-list-shipping-zones",
		Method:      http.MethodGet,
		Path:        "/v1/admin/shipping-zones",
		Summary:     "All shipping zones (admin only)",
	}, h.AdminZones)

	huma.Register(api, huma.Operation{
		OperationID: "admin-update-shipping-zone",
		Method:      http.MethodPatch,
		Path:        "/v1/admin/shipping-zones/{id}",
		Summary:     "Update a shipping zone's name, fees or active state (admin only)",
	}, h.UpdateZone)
}

type GetSettingInput struct {
	Key string `path:"key"`
}

type SettingOutput struct {
	Body struct {
		Value json.RawMessage `json:"value"`
	}
}

func (h *Handlers) GetSetting(ctx context.Context, input *GetSettingInput) (*SettingOutput, error) {
	value, err := h.repo.Setting(ctx, input.Key)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load setting", err)
	}
	if value == nil {
		return nil, huma.Error404NotFound("setting not found")
	}
	out := &SettingOutput{}
	out.Body.Value = value
	return out, nil
}

type SaveSettingInput struct {
	Authorization string `header:"Authorization"`
	Key           string `path:"key"`
	Body          struct {
		Value json.RawMessage `json:"value"`
	}
}

type successOutput struct {
	Body struct {
		Success bool `json:"success"`
	}
}

func (h *Handlers) SaveSetting(ctx context.Context, input *SaveSettingInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if err := h.repo.UpsertSetting(ctx, input.Key, input.Body.Value); err != nil {
		return nil, huma.Error500InternalServerError("failed to save setting", err)
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}

type ZonesOutput struct {
	Body []ShippingZone
}

func (h *Handlers) PublicZones(ctx context.Context, input *struct{}) (*ZonesOutput, error) {
	zones, err := h.repo.ShippingZones(ctx, true)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load shipping zones", err)
	}
	return &ZonesOutput{Body: zones}, nil
}

type adminAuthInput struct {
	Authorization string `header:"Authorization"`
}

func (h *Handlers) AdminZones(ctx context.Context, input *adminAuthInput) (*ZonesOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	zones, err := h.repo.ShippingZones(ctx, false)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load shipping zones", err)
	}
	return &ZonesOutput{Body: zones}, nil
}

type UpdateZoneInput struct {
	Authorization string `header:"Authorization"`
	ID            string `path:"id"`
	Body          struct {
		Name        *string  `json:"name,omitempty"`
		StandardFee *float64 `json:"standard_fee,omitempty"`
		ExpressFee  *float64 `json:"express_fee,omitempty"`
		IsActive    *bool    `json:"is_active,omitempty"`
	}
}

func (h *Handlers) UpdateZone(ctx context.Context, input *UpdateZoneInput) (*successOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if err := h.repo.UpdateShippingZone(ctx, input.ID, input.Body.Name, input.Body.StandardFee, input.Body.ExpressFee, input.Body.IsActive); err != nil {
		return nil, huma.Error500InternalServerError("failed to update shipping zone", err)
	}
	out := &successOutput{}
	out.Body.Success = true
	return out, nil
}
