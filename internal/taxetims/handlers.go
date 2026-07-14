package taxetims

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"gizmojunction/backend/internal/auth"
)

// Deps bundles everything the handlers need — kept separate from the
// worker's own deps struct since handlers additionally need the river
// client (to enqueue) and the internal shared-secret.
type Deps struct {
	Repo               *Repo
	RiverClient        *river.Client[pgx.Tx]
	InternalSecret     string
	DefaultTaxpayerPIN string
}

// enqueueSubmission is the one code path shared by all three entry points
// (internal webhook call, admin manual issue, admin retry): idempotently
// ensure a PENDING tax_invoices row exists for the order and a submission
// job is in flight for it. Returns the tax_invoices id.
func enqueueSubmission(ctx context.Context, deps Deps, orderID string) (string, error) {
	order, err := deps.Repo.LoadOrder(ctx, orderID)
	if err != nil {
		return "", err
	}

	taxpayerPIN := deps.DefaultTaxpayerPIN
	if taxpayerPIN == "" {
		taxpayerPIN = "A000000000X" // matches the original mock's fallback
	}
	taxAmount := order.TotalAmount * 0.16 / 1.16 // VAT-inclusive total -> tax portion, matching worker's line-item math

	id, err := deps.Repo.InsertPending(ctx, orderID, taxpayerPIN, order.TotalAmount, taxAmount)
	if err != nil && !errors.Is(err, ErrAlreadyExists) {
		return "", err
	}
	alreadyExisted := errors.Is(err, ErrAlreadyExists)

	if alreadyExisted {
		// Idempotent path: an invoice already exists for this order. Only
		// re-enqueue if it's in a state that can still move forward —
		// don't re-submit an already-ISSUED invoice (FR-OSCU-05).
		existing, loadErr := deps.Repo.GetByID(ctx, id)
		if loadErr != nil {
			return "", loadErr
		}
		if existing.Status == "ISSUED" {
			return id, nil
		}
	}

	if _, err := deps.RiverClient.Insert(ctx, TaxInvoiceSubmitArgs{TaxInvoiceID: id, OrderID: orderID}, nil); err != nil {
		return "", err
	}
	return id, nil
}

// RegisterInternal wires the shared-secret-protected endpoint called by the
// Deno payment webhooks (paystack-webhook, mpesa-callback) — same
// X-Internal-Secret pattern as backend/internal/storage/handlers.go.
func RegisterInternal(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /v1/internal/tax-invoices", func(w http.ResponseWriter, r *http.Request) {
		if deps.InternalSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Secret")), []byte(deps.InternalSecret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			OrderID string `json:"order_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.OrderID == "" {
			http.Error(w, "order_id is required", http.StatusBadRequest)
			return
		}

		id, err := enqueueSubmission(r.Context(), deps, body.OrderID)
		if err != nil {
			http.Error(w, "enqueue failed: "+err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"tax_invoice_id": id})
	})
}

type IssueInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		OrderID string `json:"order_id"`
	}
}

type IssueOutput struct {
	Body struct {
		TaxInvoiceID string `json:"tax_invoice_id"`
	}
}

type InitializeInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Environment string `json:"environment"` // "sandbox" | "production"
		TIN         string `json:"tin"`
		BhfID       string `json:"bhf_id"`
		DvcSrlNo    string `json:"dvc_srl_no"`
	}
}

type InitializeOutput struct {
	Body struct {
		SdcID string `json:"sdc_id"`
		MrcNo string `json:"mrc_no"`
	}
}

// RegisterAdmin wires the two ADMIN-only endpoints: manual issue/retry
// (replaces the admin Tax page's old supabase.functions.invoke call) and
// one-time device initialization.
func RegisterAdmin(api huma.API, deps Deps, authSvc *auth.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "admin-tax-invoice-issue",
		Method:      "POST",
		Path:        "/v1/admin/tax/invoices/issue",
		Summary:     "Manually issue or retry a KRA eTIMS tax invoice for an order",
	}, func(ctx context.Context, input *IssueInput) (*IssueOutput, error) {
		if _, err := authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
			return nil, err
		}
		if input.Body.OrderID == "" {
			return nil, huma.Error400BadRequest("order_id is required")
		}

		// If a FAILED/FAILED_FINAL row already exists, reset it so the
		// worker treats this as a fresh attempt rather than silently
		// no-op'ing on the "already exists" idempotency path.
		if existingID, lookupErr := deps.Repo.GetIDByOrderID(ctx, input.Body.OrderID); lookupErr == nil {
			existing, err := deps.Repo.GetByID(ctx, existingID)
			if err == nil && (existing.Status == "FAILED" || existing.Status == "FAILED_FINAL") {
				if err := deps.Repo.ResetForRetry(ctx, existingID); err != nil {
					return nil, huma.Error500InternalServerError("reset for retry: " + err.Error())
				}
			}
		}

		id, err := enqueueSubmission(ctx, deps, input.Body.OrderID)
		if err != nil {
			return nil, huma.Error502BadGateway("enqueue failed: " + err.Error())
		}

		out := &IssueOutput{}
		out.Body.TaxInvoiceID = id
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-tax-oscu-initialize",
		Method:      "POST",
		Path:        "/v1/admin/tax/oscu/initialize",
		Summary:     "One-time KRA OSCU device registration (run once real KRA credentials exist)",
	}, func(ctx context.Context, input *InitializeInput) (*InitializeOutput, error) {
		if _, err := authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
			return nil, err
		}
		if input.Body.TIN == "" || input.Body.BhfID == "" || input.Body.DvcSrlNo == "" {
			return nil, huma.Error400BadRequest("tin, bhf_id, and dvc_srl_no are all required")
		}

		env := input.Body.Environment
		if env != "production" {
			env = "sandbox"
		}

		client := NewClient(env)
		resp, err := client.Initialize(ctx, InitializeRequest{TIN: input.Body.TIN, BhfID: input.Body.BhfID, DvcSrlNo: input.Body.DvcSrlNo})
		if err != nil {
			return nil, huma.Error502BadGateway("OSCU initialization failed: " + err.Error())
		}

		cfg := DeviceConfig{
			Environment: env,
			TIN:         input.Body.TIN,
			BhfID:       input.Body.BhfID,
			DvcSrlNo:    input.Body.DvcSrlNo,
			CMCKey:      resp.Data.CmcKey,
			SdcID:       &resp.Data.SdcID,
			MrcNo:       &resp.Data.MrcNo,
		}
		if err := deps.Repo.SaveDeviceConfig(ctx, cfg); err != nil {
			return nil, huma.Error500InternalServerError("save device config: " + err.Error())
		}

		out := &InitializeOutput{}
		out.Body.SdcID = resp.Data.SdcID
		out.Body.MrcNo = resp.Data.MrcNo
		return out, nil
	})
}
