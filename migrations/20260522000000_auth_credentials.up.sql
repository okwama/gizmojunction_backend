-- Phase 4: the Go backend becomes the owner of authentication. profiles
-- gains its own credential storage (password_hash/password_algo) instead
-- of relying on Supabase's auth.users/GoTrue. password_algo defaults to
-- 'argon2id' for new signups; legacy accounts migrated from a real
-- Supabase export would land with password_algo='bcrypt' and get
-- transparently upgraded to argon2id on first successful login (see
-- backend/internal/auth) — there's no real exported password data to
-- migrate yet, so this path is built and tested with synthetic data only.

ALTER TABLE public.profiles
    ADD COLUMN IF NOT EXISTS password_hash text,
    ADD COLUMN IF NOT EXISTS password_algo text NOT NULL DEFAULT 'argon2id';

CREATE TABLE IF NOT EXISTS public.refresh_tokens (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    profile_id uuid NOT NULL REFERENCES public.profiles(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_profile_id ON public.refresh_tokens (profile_id);
