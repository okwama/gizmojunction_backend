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
	Orders   *pgxpool.Pool // the transitional Supabase orders pool
	River    *river.Client[pgx.Tx]
	Taxetims *taxetims.Deps // nil when eTIMS isn't configured
	Cfg      Config
}

func (c Config) mpesaConfigured() bool {
	return c.MpesaConsumerKey != "" && c.MpesaConsumerSecret != "" && c.MpesaPasskey != "" && c.BackendPublicURL != ""
}

// firePaidSideEffects runs once per successful transition to paid: the
// buyer/admin notification emails (river) and the KRA eTIMS submission —
// both in-process, replacing the Deno webhooks' fire-and-forget HTTP hop.
func (d *Deps) firePaidSideEffects(ctx context.Context, orderID string) {
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
