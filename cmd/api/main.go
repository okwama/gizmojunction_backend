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

	"gizmojunction/backend/internal/ai"
	"gizmojunction/backend/internal/auth"
	"gizmojunction/backend/internal/catalog"
	"gizmojunction/backend/internal/config"
	"gizmojunction/backend/internal/db"
	"gizmojunction/backend/internal/jobs"
	"gizmojunction/backend/internal/storage"
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
	if cfg.SupabaseDatabaseURL != "" {
		supabasePool, err := db.Connect(ctx, cfg.SupabaseDatabaseURL)
		if err != nil {
			log.Fatalf("connect to supabase database: %v", err)
		}
		defer supabasePool.Close()
		taxetimsRepo = taxetims.NewRepo(supabasePool)
	} else {
		log.Println("SUPABASE_DATABASE_URL not configured — KRA eTIMS endpoints disabled")
	}

	riverClient, err := jobs.NewClient(jobs.Deps{
		Pool:       pool,
		Email:      emailSender,
		SiteURL:    cfg.SiteURL,
		AdminEmail: cfg.AdminEmail,
	}, func(workers *river.Workers) {
		if taxetimsRepo == nil {
			return
		}
		river.AddWorker(workers, &taxetims.TaxInvoiceSubmitWorker{
			Repo:                   taxetimsRepo,
			Client:                 taxetims.NewClient(cfg.KRAEnv),
			Email:                  emailSender,
			AdminEmail:             cfg.AdminEmail,
			SupabaseURL:            cfg.SupabaseURL,
			SupabaseServiceRoleKey: cfg.SupabaseServiceRoleKey,
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
	catalog.Register(api, catalog.NewRepo(pool))

	authSvc := auth.NewService(auth.NewRepo(pool), cfg.JWTSecret)
	auth.Register(api, authSvc)
	auth.RegisterAdminUsers(api, authSvc)

	aiCfg := ai.Config{GeminiAPIKey: cfg.GeminiAPIKey, GroqAPIKey: cfg.GroqAPIKey}
	ai.RegisterGenerateBlog(api, aiCfg, authSvc)
	ai.RegisterSuggestName(api, aiCfg, authSvc)

	if cfg.R2AccountID != "" && cfg.R2AccessKeyID != "" && cfg.R2SecretKey != "" && cfg.R2Bucket != "" {
		r2Client, err := storage.NewClient(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretKey, cfg.R2Bucket)
		if err != nil {
			log.Fatalf("init R2 client: %v", err)
		}
		storage.Register(mux, r2Client, cfg.InternalSecret)
	} else {
		log.Println("R2 not configured (R2_ACCOUNT_ID/R2_ACCESS_KEY_ID/R2_SECRET_ACCESS_KEY/R2_BUCKET) — document upload/redirect endpoints disabled")
	}

	if taxetimsRepo != nil {
		taxetimsDeps := taxetims.Deps{
			Repo:               taxetimsRepo,
			RiverClient:        riverClient,
			InternalSecret:     cfg.InternalSecret,
			DefaultTaxpayerPIN: cfg.KRADefaultTaxpayerPIN,
		}
		taxetims.RegisterInternal(mux, taxetimsDeps)
		taxetims.RegisterAdmin(api, taxetimsDeps, authSvc)
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
