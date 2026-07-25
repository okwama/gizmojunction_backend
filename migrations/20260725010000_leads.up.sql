-- Contact page and Register-for-the-Club forms had no backend at all — both
-- called e.preventDefault() and flipped a local "submitted" flag with no
-- network call, so submissions went nowhere (see UI_UX_AUDIT.md storefront
-- audit follow-up). These are the minimal tables they need. Public-write-only
-- via the Go API; no RLS, matching newsletter_subscribers and the rest of
-- this Neon-backed schema — access control lives in the Go handler.
CREATE TABLE IF NOT EXISTS public.contact_messages (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    email        text NOT NULL,
    subject      text,
    message      text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.club_registrations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    email        text NOT NULL,
    phone        text,
    location     text,
    interests    text[] NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);
