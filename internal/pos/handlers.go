// Package pos covers in-store (walk-in) sales — Section 8.1 of
// ADMIN_RETAIL_OPS_REVIEW.md, "the critical gap": walk-in sales had no way
// to be recorded, silently desyncing stock between the shop floor and the
// online catalog.
//
// This package is orchestration only — no new core logic in orders or
// taxetims. A POS sale is a real orders row (the receipt + KRA eTIMS
// pipeline is hard-keyed to orders.id, there's no lighter-weight data
// shape available), created via the same orders.Repo.CreateOrder every
// online order goes through, then immediately marked paid and stock-
// decremented since a cashier has already confirmed payment in person —
// unlike online M-Pesa/Paystack, which start pending until a webhook
// confirms them. This mirrors how internal/payments already composes
// orders.Repo from outside its own package rather than adding new coupling
// into orders itself.
package pos

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"gizmojunction/backend/internal/auth"
	"gizmojunction/backend/internal/orders"
	"gizmojunction/backend/internal/payments"
	"gizmojunction/backend/internal/taxetims"
)

type Handlers struct {
	orders   *orders.Repo
	taxetims taxetims.Deps
	authSvc  *auth.Service
	// payments is nil-safe by contract: every caller in main.go always wires
	// a real *payments.Deps, but the mpesa-prompt handler still checks for
	// nil defensively since it's the only pos handler that reaches into
	// another package's live integration rather than pure orchestration.
	payments *payments.Deps
}

func Register(api huma.API, ordersRepo *orders.Repo, taxetimsDeps taxetims.Deps, authSvc *auth.Service, paymentsDeps *payments.Deps) {
	h := &Handlers{orders: ordersRepo, taxetims: taxetimsDeps, authSvc: authSvc, payments: paymentsDeps}
	huma.Register(api, huma.Operation{
		OperationID: "pos-create-sale",
		Method:      http.MethodPost,
		Path:        "/v1/admin/pos/sales",
		Summary:     "Record an in-store sale — creates the order, decrements stock, marks it paid, and enqueues eTIMS submission (admin only)",
	}, h.CreateSale)
	huma.Register(api, huma.Operation{
		OperationID: "pos-mpesa-prompt",
		Method:      http.MethodPost,
		Path:        "/v1/admin/pos/sales/{orderId}/mpesa-prompt",
		Summary:     "Send an M-Pesa STK push to the customer's phone for a pending in-store sale (admin only)",
	}, h.MpesaPrompt)
}

type SaleItem struct {
	ProductID string `json:"product_id"`
	Quantity  int32  `json:"quantity" minimum:"1"`
}

type CreateSaleInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Items []SaleItem `json:"items"`
		// mpesa_stk is the only method that doesn't complete the sale
		// synchronously — see CreateSale's branch below and MpesaPrompt.
		PaymentMethod string `json:"payment_method" enum:"cash,mpesa_till,mpesa_stk,card"`
		// Phone/KraPin are optional walk-in identity capture (§8.7: link
		// in-store and online history, and B2B buyers who want a KRA PIN
		// on record) — never required, unlike online checkout.
		Phone  string `json:"phone,omitempty" required:"false"`
		KraPin string `json:"kra_pin,omitempty" required:"false"`
		// MpesaConfirmationCode is the code from the till payment SMS —
		// folded into the order's existing payment_metadata jsonb for
		// future till-reconciliation use, not part of any compliance flow.
		MpesaConfirmationCode string `json:"mpesa_confirmation_code,omitempty" required:"false"`
	}
}

type CreateSaleOutput struct {
	Body struct {
		OrderID string `json:"order_id"`
	}
}

func (h *Handlers) CreateSale(ctx context.Context, input *CreateSaleInput) (*CreateSaleOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if len(input.Body.Items) == 0 {
		return nil, huma.Error400BadRequest("at least one item is required")
	}

	items := make([]orders.NewOrderItem, len(input.Body.Items))
	for i, it := range input.Body.Items {
		items[i] = orders.NewOrderItem{ProductID: it.ProductID, Quantity: it.Quantity}
	}

	// Walk-in sales rarely have a real shipping address; the rest of the
	// pipeline (order notifications, commercial invoices) already falls
	// back gracefully on missing fields, but a plain "Walk-in Customer"
	// first_name reads better than the generic placeholder.
	shippingAddress, err := json.Marshal(map[string]string{
		"first_name": "Walk-in Customer",
		"phone":      input.Body.Phone,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to build sale", err)
	}

	orderID, err := h.orders.CreateOrder(ctx, nil, orders.NewOrder{
		Items:           items,
		DeliveryMethod:  "in_store",
		PaymentMethod:   input.Body.PaymentMethod,
		ShippingAddress: shippingAddress,
		KraPin:          input.Body.KraPin,
	})
	if err != nil {
		if errors.Is(err, orders.ErrUnavailable) {
			return nil, huma.Error400BadRequest(err.Error())
		}
		return nil, huma.Error500InternalServerError("failed to create sale", err)
	}

	// mpesa_stk stays PENDING/unpaid here — the customer hasn't confirmed
	// anything yet. The frontend follows up with MpesaPrompt to send the
	// push, then polls the order; the same M-Pesa callback that already
	// handles online checkout (payments.handleMpesaCallback) marks it paid,
	// decrements stock, and enqueues eTIMS — no duplicate logic needed here.
	if input.Body.PaymentMethod == "mpesa_stk" {
		out := &CreateSaleOutput{}
		out.Body.OrderID = orderID
		return out, nil
	}

	if err := h.orders.DecrementStock(ctx, orderID); err != nil {
		return nil, huma.Error500InternalServerError("sale created but stock decrement failed — check order "+orderID+" manually", err)
	}

	metadata := json.RawMessage(`{}`)
	if input.Body.MpesaConfirmationCode != "" {
		if m, err := json.Marshal(map[string]string{"mpesa_confirmation_code": input.Body.MpesaConfirmationCode}); err == nil {
			metadata = m
		}
	}
	if err := h.orders.MarkInStoreSalePaid(ctx, orderID, metadata); err != nil {
		return nil, huma.Error500InternalServerError("sale created but couldn't be marked paid — check order "+orderID+" manually", err)
	}

	// Non-fatal: eTIMS is async everywhere else in this codebase too (the
	// online-payment path enqueues and moves on the same way). A failed
	// enqueue here doesn't undo the sale — it can be retried later from
	// the order detail page like any other order.
	if _, err := taxetims.EnqueueSubmission(ctx, h.taxetims, orderID); err != nil {
		log.Printf("pos: eTIMS enqueue failed for order %s: %v", orderID, err)
	}

	out := &CreateSaleOutput{}
	out.Body.OrderID = orderID
	return out, nil
}

type MpesaPromptInput struct {
	Authorization string `header:"Authorization"`
	OrderID       string `path:"orderId"`
	Body          struct {
		Phone string `json:"phone"`
	}
}

type MpesaPromptOutput struct {
	Body struct {
		Sent bool `json:"sent"`
	}
}

// MpesaPrompt sends the STK push for a POS sale created with
// payment_method="mpesa_stk". It delegates entirely to payments.Deps.StkPush
// — the exact same call the storefront checkout makes — so the push, the
// callback URL, and the eventual paid-webhook side effects (stock decrement,
// eTIMS enqueue) are one code path, not two. Composition over duplication,
// same as this package already does with orders.Repo.
func (h *Handlers) MpesaPrompt(ctx context.Context, input *MpesaPromptInput) (*MpesaPromptOutput, error) {
	if _, err := h.authSvc.RequireRole(input.Authorization, "ADMIN"); err != nil {
		return nil, err
	}
	if input.Body.Phone == "" {
		return nil, huma.Error400BadRequest("phone is required")
	}
	if h.payments == nil {
		return nil, huma.Error503ServiceUnavailable("M-Pesa is not configured on this server")
	}

	stkInput := &payments.StkPushInput{}
	stkInput.Body.Phone = input.Body.Phone
	stkInput.Body.OrderID = input.OrderID
	if _, err := h.payments.StkPush(ctx, stkInput); err != nil {
		return nil, err
	}

	out := &MpesaPromptOutput{}
	out.Body.Sent = true
	return out, nil
}
