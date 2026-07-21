-- Storefront footer "Subscribe for Exclusive Offers" form had no backend at
-- all (see GizmoJunction UI_UX_AUDIT.md punch list item 1) — this is the
-- minimal table it needs. Public-write-only via the Go API (POST
-- /v1/newsletter/subscribe); no RLS, matching the rest of this Neon-backed
-- schema — access control lives in the Go handler, not the database.
CREATE TABLE IF NOT EXISTS public.newsletter_subscribers (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          text NOT NULL UNIQUE,
    subscribed_at  timestamptz NOT NULL DEFAULT now()
);
