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
	"gizmojunction/backend/internal/taxetims"
)

type Handlers struct {
	orders   *orders.Repo
	taxetims taxetims.Deps
	authSvc  *auth.Service
}

func Register(api huma.API, ordersRepo *orders.Repo, taxetimsDeps taxetims.Deps, authSvc *auth.Service) {
	h := &Handlers{orders: ordersRepo, taxetims: taxetimsDeps, authSvc: authSvc}
	huma.Register(api, huma.Operation{
		OperationID: "pos-create-sale",
		Method:      http.MethodPost,
		Path:        "/v1/admin/pos/sales",
		Summary:     "Record an in-store sale — creates the order, decrements stock, marks it paid, and enqueues eTIMS submission (admin only)",
	}, h.CreateSale)
}

type SaleItem struct {
	ProductID string `json:"product_id"`
	Quantity  int32  `json:"quantity" minimum:"1"`
}

type CreateSaleInput struct {
	Authorization string `header:"Authorization"`
	Body          struct {
		Items         []SaleItem `json:"items"`
		PaymentMethod string     `json:"payment_method" enum:"cash,mpesa_till"`
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
