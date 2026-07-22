package payments

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"gizmojunction/backend/internal/jobs"
	"gizmojunction/backend/internal/taxetims"
)

type Config struct {
	MpesaConsumerKey    string
	MpesaConsumerSecret string
	MpesaPasskey        string
	MpesaShortcode      string
	MpesaTillNumber     string
	MpesaEnvironment    string
	PaystackSecretKey   string
	BackendPublicURL    string
	SiteURL             string
}

type Deps struct {
	Orders   *pgxpool.Pool // Neon — same database as the catalog
	River    *river.Client[pgx.Tx]
	Taxetims *taxetims.Deps // nil when eTIMS isn't configured
	Cfg      Config
}

// orderChargeAmount loads the authoritative amount to charge for an order —
// initiation never trusts a client-supplied amount. Already-paid orders are
// rejected so a stray re-initiation can't double-charge.
func (d *Deps) orderChargeAmount(ctx context.Context, orderID string) (float64, error) {
	var total float64
	var paymentStatus *string
	err := d.Orders.QueryRow(ctx, `
		SELECT total_amount::float8, payment_status FROM orders WHERE id = $1`, orderID).
		Scan(&total, &paymentStatus)
	if err != nil {
		return 0, huma.Error404NotFound("Order not found")
	}
	if paymentStatus != nil && *paymentStatus == "paid" {
		return 0, huma.Error400BadRequest("This order is already paid")
	}
	if total <= 0 {
		return 0, huma.Error400BadRequest("Order has no payable amount")
	}
	return total, nil
}

func (c Config) mpesaConfigured() bool {
	return c.MpesaConsumerKey != "" && c.MpesaConsumerSecret != "" && c.MpesaPasskey != "" && c.BackendPublicURL != ""
}

// firePaidSideEffects runs once per successful transition to paid: stock
// deduction, the buyer/admin notification emails (river), and the KRA
// eTIMS submission — all in-process, replacing the Deno webhooks'
// fire-and-forget HTTP hop.
func (d *Deps) firePaidSideEffects(ctx context.Context, orderID string) {
	if err := decrementStock(ctx, d.Orders, orderID); err != nil {
		log.Printf("payments: failed to decrement stock for %s: %v", orderID, err)
	}
	if d.River != nil {
		if _, err := d.River.Insert(ctx, jobs.OrderNotificationArgs{OrderID: orderID}, nil); err != nil {
			log.Printf("payments: failed to enqueue order notification for %s: %v", orderID, err)
		}
	}
	if d.Taxetims != nil {
		if _, err := taxetims.EnqueueSubmission(ctx, *d.Taxetims, orderID); err != nil {
			log.Printf("payments: failed to enqueue tax invoice for %s: %v", orderID, err)
		}
	}
}

// decrementStock deducts the order's quantities from product stock, guarded
// by orders.stock_decremented so it's idempotent (mirrors
// orders.Repo.DecrementStock without the package dependency).
func decrementStock(ctx context.Context, pool *pgxpool.Pool, orderID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var already bool
	if err := tx.QueryRow(ctx, `
		SELECT stock_decremented FROM orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&already); err != nil {
		return err
	}
	if already {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE products p SET stock_quantity = GREATEST(p.stock_quantity - oi.quantity, 0)
		FROM order_items oi
		WHERE oi.order_id = $1 AND p.id = oi.product_id`, orderID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET stock_decremented = true WHERE id = $1`, orderID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Register wires whichever providers are configured. Initiation endpoints
// go through huma (JSON, called by the storefront); the two webhooks live
// on the raw mux because providers post to them directly and Paystack's
// signature check needs the exact raw body.
func Register(api huma.API, mux *http.ServeMux, deps *Deps) {
	if deps.Cfg.mpesaConfigured() {
		huma.Register(api, huma.Operation{
			OperationID: "mpesa-stk-push",
			Method:      http.MethodPost,
			Path:        "/v1/payments/mpesa/stk-push",
			Summary:     "Initiate an M-Pesa STK push for an order",
		}, deps.StkPush)
		mux.HandleFunc("POST /v1/payments/mpesa/callback", deps.handleMpesaCallback)
	} else {
		log.Println("M-Pesa credentials not fully configured (MPESA_CONSUMER_KEY/SECRET/PASSKEY + BACKEND_PUBLIC_URL) — M-Pesa endpoints disabled")
	}

	if deps.Cfg.PaystackSecretKey != "" {
		huma.Register(api, huma.Operation{
			OperationID: "paystack-init",
			Method:      http.MethodPost,
			Path:        "/v1/payments/paystack/init",
			Summary:     "Initialize a Paystack card transaction for an order",
		}, deps.PaystackInit)
		mux.HandleFunc("POST /v1/payments/paystack/webhook", deps.handlePaystackWebhook)
	} else {
		log.Println("PAYSTACK_SECRET_KEY not configured — Paystack endpoints disabled")
	}
}
