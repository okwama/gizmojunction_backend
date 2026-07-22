package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL    string
	Port           string
	CORSOrigin     string
	SiteURL        string
	ResendAPIKey   string
	AdminEmail     string
	GeminiAPIKey   string
	GroqAPIKey     string
	R2AccountID    string
	R2AccessKeyID  string
	R2SecretKey    string
	R2Bucket       string
	InternalSecret string
	JWTSecret      string

	// SupabaseDatabaseURL is no longer used by the API server — all domains
	// run on DATABASE_URL (Neon). Kept only as the default --source for
	// cmd/migrate-data delta re-copies during decommissioning.
	SupabaseDatabaseURL    string
	SupabaseURL            string
	SupabaseServiceRoleKey string
	KRADefaultTaxpayerPIN  string
	KRAEnv                 string // "sandbox" (default) | "production"

	// BackendPublicURL is this API's own public base URL (e.g.
	// https://gizmojunction-backend.onrender.com) — used to build the stable
	// /v1/documents/... proxy URLs stored on re-hosted import images. Empty
	// disables image re-hosting during imports (originals kept).
	BackendPublicURL string

	// Payment provider credentials (Phase 6) — previously Supabase Edge
	// Function secrets. Missing M-Pesa or Paystack credentials disable that
	// provider's endpoints individually.
	MpesaConsumerKey    string
	MpesaConsumerSecret string
	MpesaPasskey        string
	MpesaShortcode      string
	MpesaTillNumber     string
	MpesaEnvironment    string // "sandbox" (default) | "production"
	PaystackSecretKey   string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is not set")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	corsOrigin := os.Getenv("CORS_ORIGIN")
	if corsOrigin == "" {
		corsOrigin = "http://localhost:5173,http://localhost:5174,https://gizmojunction.com,https://www.gizmojunction.com"
	}

	siteURL := os.Getenv("PUBLIC_SITE_URL")
	if siteURL == "" {
		siteURL = "https://www.gizmojunction.com"
	}

	adminEmail := os.Getenv("ADMIN_NOTIFICATION_EMAIL")
	if adminEmail == "" {
		adminEmail = "admin@gizmojunction.com"
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is not set")
	}

	return &Config{
		DatabaseURL:    dbURL,
		Port:           port,
		CORSOrigin:     corsOrigin,
		SiteURL:        siteURL,
		ResendAPIKey:   os.Getenv("RESEND_API_KEY"),
		AdminEmail:     adminEmail,
		GeminiAPIKey:   os.Getenv("GEMINI_API_KEY"),
		GroqAPIKey:     os.Getenv("GROQ_API_KEY"),
		R2AccountID:    os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:  os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretKey:    os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Bucket:       os.Getenv("R2_BUCKET"),
		InternalSecret: os.Getenv("INTERNAL_UPLOAD_SECRET"),
		JWTSecret:      jwtSecret,

		SupabaseDatabaseURL:    os.Getenv("SUPABASE_DATABASE_URL"),
		SupabaseURL:            os.Getenv("SUPABASE_URL"),
		SupabaseServiceRoleKey: os.Getenv("SUPABASE_SERVICE_ROLE_KEY"),
		KRADefaultTaxpayerPIN:  os.Getenv("KRA_PIN"),
		KRAEnv:                 os.Getenv("KRA_ENV"),

		BackendPublicURL: os.Getenv("BACKEND_PUBLIC_URL"),

		MpesaConsumerKey:    os.Getenv("MPESA_CONSUMER_KEY"),
		MpesaConsumerSecret: os.Getenv("MPESA_CONSUMER_SECRET"),
		MpesaPasskey:        os.Getenv("MPESA_PASSKEY"),
		MpesaShortcode:      envOr("MPESA_SHORTCODE", "174379"),
		MpesaTillNumber:     envOr("MPESA_TILL_NUMBER", "3304235"),
		MpesaEnvironment:    envOr("MPESA_ENVIRONMENT", "sandbox"),
		PaystackSecretKey:   os.Getenv("PAYSTACK_SECRET_KEY"),
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
