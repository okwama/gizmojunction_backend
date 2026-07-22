package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/riverqueue/river"

	"gizmojunction/backend/internal/account"
	"gizmojunction/backend/internal/ai"
	"gizmojunction/backend/internal/audit"
	"gizmojunction/backend/internal/auth"
	"gizmojunction/backend/internal/catalog"
	"gizmojunction/backend/internal/config"
	"gizmojunction/backend/internal/db"
	"gizmojunction/backend/internal/erp"
	"gizmojunction/backend/internal/importer"
	"gizmojunction/backend/internal/jobs"
	"gizmojunction/backend/internal/newsletter"
	"gizmojunction/backend/internal/orders"
	"gizmojunction/backend/internal/payments"
	"gizmojunction/backend/internal/search"
	"gizmojunction/backend/internal/storage"
	"gizmojunction/backend/internal/store"
	"gizmojunction/backend/internal/suppliersync"
	"gizmojunction/backend/internal/taxetims"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	emailSender := jobs.NewEmailSender(cfg.ResendAPIKey)

	// Orders, payments, and eTIMS all live in Neon now — the transitional
	// second Supabase pool is gone (data copied via cmd/migrate-data).
	taxetimsRepo := taxetims.NewRepo(pool)

	// r2Client is created before the river client because the tax worker
	// now renders receipt PDFs in-process (Phase 6) and needs storage. It
	// stays nil when R2 isn't configured — receipt generation and the LPO
	// endpoint degrade gracefully.
	var r2Client *storage.Client
	if cfg.R2AccountID != "" && cfg.R2AccessKeyID != "" && cfg.R2SecretKey != "" && cfg.R2Bucket != "" {
		r2Client, err = storage.NewClient(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretKey, cfg.R2Bucket)
		if err != nil {
			log.Fatalf("init R2 client: %v", err)
		}
	} else {
		log.Println("R2 not configured (R2_ACCOUNT_ID/R2_ACCESS_KEY_ID/R2_SECRET_ACCESS_KEY/R2_BUCKET) — document upload/redirect endpoints disabled")
	}

	receiptDeps := taxetims.ReceiptDeps{Repo: taxetimsRepo, Catalog: pool, Store: r2Client}

	riverClient, err := jobs.NewClient(jobs.Deps{
		Pool:       pool,
		OrdersPool: pool,
		Email:      emailSender,
		SiteURL:    cfg.SiteURL,
		AdminEmail: cfg.AdminEmail,
	}, func(workers *river.Workers) {
		river.AddWorker(workers, &taxetims.TaxInvoiceSubmitWorker{
			Repo:       taxetimsRepo,
			Client:     taxetims.NewClient(cfg.KRAEnv),
			Email:      emailSender,
			AdminEmail: cfg.AdminEmail,
			Receipt:    receiptDeps,
		})
	})
	if err != nil {
		log.Fatalf("init job client: %v", err)
	}
	if err := riverClient.Start(ctx); err != nil {
		log.Fatalf("start job client: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = riverClient.Stop(stopCtx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status := "ok"
		code := http.StatusOK
		if err := pool.Ping(pingCtx); err != nil {
			status = "database unreachable"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
	})

	api := humago.New(mux, huma.DefaultConfig("Gizmo Junction API", "0.1.0"))
	catalogRepo := catalog.NewRepo(pool)
	catalog.Register(api, catalogRepo)
	catalog.RegisterExtra(api, catalogRepo)

	authSvc := auth.NewService(auth.NewRepo(pool), cfg.JWTSecret)
	auth.Register(api, authSvc)
	auth.RegisterAdminUsers(api, authSvc)

	// Product search runs directly against Postgres (full-text + pg_trgm),
	// so it's always available — no external service to configure.
	search.Register(api, pool)

	catalog.RegisterAdmin(api, catalogRepo, authSvc)

	aiCfg := ai.Config{GeminiAPIKey: cfg.GeminiAPIKey, GroqAPIKey: cfg.GroqAPIKey}
	ai.RegisterGenerateBlog(api, aiCfg, authSvc)
	ai.RegisterSuggestName(api, aiCfg, authSvc)

	// r2Client was created earlier (before the river client, which needs it
	// for in-process receipt PDFs); only the HTTP routes register here.
	if r2Client != nil {
		storage.Register(mux, r2Client, cfg.InternalSecret)
	}

	erp.Register(api, erp.NewRepo(pool), authSvc, r2Client)
	store.Register(api, store.NewRepo(pool), authSvc)
	newsletter.Register(api, newsletter.NewRepo(pool))
	suppliersync.Register(api, mux, suppliersync.NewRepo(pool), authSvc)
	audit.Register(api, pool, authSvc)
	account.Register(api, pool, authSvc)
	importer.Register(api, pool, authSvc, r2Client, importer.Config{
		GeminiAPIKey:     cfg.GeminiAPIKey,
		BackendPublicURL: cfg.BackendPublicURL,
	})

	orders.Register(api, orders.NewRepo(pool, pool), authSvc, riverClient)

	taxetimsDeps := taxetims.Deps{
		Repo:               taxetimsRepo,
		RiverClient:        riverClient,
		InternalSecret:     cfg.InternalSecret,
		DefaultTaxpayerPIN: cfg.KRADefaultTaxpayerPIN,
	}
	taxetims.RegisterInternal(mux, taxetimsDeps)
	taxetims.RegisterAdmin(api, taxetimsDeps, authSvc)
	taxetims.RegisterReceipts(api, receiptDeps, authSvc)

	// Logged once at startup, not per-request — sandbox vs. production is
	// silent otherwise (sandbox accepts STK requests and calls back on its
	// own simulated timer without ever reaching a real phone, which looks
	// identical to a real failed payment from the checkout page).
	log.Printf("payments: M-Pesa environment=%s till=%s configured=%v; Resend email configured=%v",
		cfg.MpesaEnvironment, cfg.MpesaTillNumber, cfg.MpesaConsumerKey != "" && cfg.MpesaPasskey != "", cfg.ResendAPIKey != "")

	payments.Register(api, mux, &payments.Deps{
		Orders:   pool,
		River:    riverClient,
		Taxetims: &taxetimsDeps,
		Cfg: payments.Config{
			MpesaConsumerKey:    cfg.MpesaConsumerKey,
			MpesaConsumerSecret: cfg.MpesaConsumerSecret,
			MpesaPasskey:        cfg.MpesaPasskey,
			MpesaShortcode:      cfg.MpesaShortcode,
			MpesaTillNumber:     cfg.MpesaTillNumber,
			MpesaEnvironment:    cfg.MpesaEnvironment,
			PaystackSecretKey:   cfg.PaystackSecretKey,
			BackendPublicURL:    cfg.BackendPublicURL,
			SiteURL:             cfg.SiteURL,
		},
	})

	handler := corsMiddleware(cfg.CORSOrigin, mux)

	log.Printf("listening on :%s", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, handler); err != nil {
		log.Fatal(err)
	}
}

// corsMiddleware allows the SvelteKit storefront's origin(s) to call these
// endpoints — the +page.js load functions this replaces run in the browser
// on client-side navigation, not only during SSR.
//
// allowedOrigins is comma-separated (CORS_ORIGIN="https://gizmojunction.com,http://localhost:5173")
// since Access-Control-Allow-Origin can only ever echo back one origin per
// response — a literal comma-joined value there would just make browsers
// reject it. Instead this matches the request's own Origin header against
// the allow-list and echoes back only that one when it matches.
//
// No special-casing for a missing Origin header: SvelteKit's own SSR `load`
// fetch always sets one itself (to its own app's origin — see
// @sveltejs/kit/src/runtime/server/fetch.js, `request.headers.set('origin',
// event.url.origin)`) for exactly this kind of cross-origin GET request, so
// plain matching already works correctly whether that origin is
// localhost:5173 in dev or the real domain once the frontend is deployed.
func corsMiddleware(allowedOrigins string, next http.Handler) http.Handler {
	allowed := make(map[string]bool)
	for _, origin := range strings.Split(allowedOrigins, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowed[origin] = true
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
