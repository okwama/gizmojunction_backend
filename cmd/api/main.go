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
	"github.com/jackc/pgx/v5/pgxpool"
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

	// taxetimsRepo/taxetimsDeps are non-nil only when SUPABASE_DATABASE_URL
	// is set — orders/tax_invoices still live in Supabase, not Neon (Phase
	// 5 hasn't moved them yet), so eTIMS endpoints are simply unregistered
	// rather than erroring when that connection isn't configured, matching
	// the R2/storage pattern below.
	var taxetimsRepo *taxetims.Repo
	var supabasePool *pgxpool.Pool
	if cfg.SupabaseDatabaseURL != "" {
		supabasePool, err = db.Connect(ctx, cfg.SupabaseDatabaseURL)
		if err != nil {
			log.Fatalf("connect to supabase database: %v", err)
		}
		defer supabasePool.Close()
		taxetimsRepo = taxetims.NewRepo(supabasePool)
	} else {
		log.Println("SUPABASE_DATABASE_URL not configured — KRA eTIMS endpoints disabled")
	}

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

	var receiptDeps taxetims.ReceiptDeps
	if taxetimsRepo != nil {
		receiptDeps = taxetims.ReceiptDeps{Repo: taxetimsRepo, Catalog: pool, Store: r2Client}
	}

	riverClient, err := jobs.NewClient(jobs.Deps{
		Pool:       pool,
		OrdersPool: supabasePool,
		Email:      emailSender,
		SiteURL:    cfg.SiteURL,
		AdminEmail: cfg.AdminEmail,
	}, func(workers *river.Workers) {
		if taxetimsRepo == nil {
			return
		}
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

	// Search (Meilisearch) is optional infra, same "disabled until
	// configured" pattern as R2 storage and KRA eTIMS below — product
	// writes just skip indexing and /v1/search is never registered if
	// MEILI_HOST isn't set.
	var productIndexer catalog.ProductIndexer
	if cfg.MeiliHost != "" {
		searchClient := search.NewClient(cfg.MeiliHost, cfg.MeiliAPIKey, pool)
		if err := searchClient.EnsureIndex(ctx); err != nil {
			log.Printf("search: failed to configure Meilisearch index settings: %v", err)
		}
		search.Register(api, searchClient)
		search.RegisterAdmin(api, searchClient, authSvc)
		productIndexer = searchClient
	} else {
		log.Println("MEILI_HOST not configured — product search endpoints disabled")
	}

	catalog.RegisterAdmin(api, catalogRepo, authSvc, productIndexer)

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
	suppliersync.Register(api, mux, suppliersync.NewRepo(pool), authSvc, productIndexer)
	audit.Register(api, pool, authSvc)
	account.Register(api, pool, authSvc)
	importer.Register(api, pool, authSvc, r2Client, productIndexer, importer.Config{
		GeminiAPIKey:     cfg.GeminiAPIKey,
		BackendPublicURL: cfg.BackendPublicURL,
	})

	// Orders live in Supabase until the Phase 6 payment-webhook cutover, so
	// the whole orders domain rides the same second pool as eTIMS and is
	// disabled without it.
	if supabasePool != nil {
		orders.Register(api, orders.NewRepo(supabasePool, pool), authSvc)
	} else {
		log.Println("SUPABASE_DATABASE_URL not configured — orders endpoints disabled")
	}

	var taxetimsDepsPtr *taxetims.Deps
	if taxetimsRepo != nil {
		taxetimsDeps := taxetims.Deps{
			Repo:               taxetimsRepo,
			RiverClient:        riverClient,
			InternalSecret:     cfg.InternalSecret,
			DefaultTaxpayerPIN: cfg.KRADefaultTaxpayerPIN,
		}
		taxetims.RegisterInternal(mux, taxetimsDeps)
		taxetims.RegisterAdmin(api, taxetimsDeps, authSvc)
		taxetims.RegisterReceipts(api, receiptDeps, authSvc)
		taxetimsDepsPtr = &taxetimsDeps
	}

	// Payments (Phase 6) ride the same transitional Supabase orders pool.
	// M-Pesa flips per STK push (its callback URL is in each request);
	// Paystack flips when the dashboard webhook URL is repointed here.
	if supabasePool != nil {
		payments.Register(api, mux, &payments.Deps{
			Orders:   supabasePool,
			River:    riverClient,
			Taxetims: taxetimsDepsPtr,
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
	} else {
		log.Println("SUPABASE_DATABASE_URL not configured — payment endpoints disabled")
	}

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
