-- Compatibility shim so the Supabase-authored schema dump (public_schema.sql)
-- and the incremental migrations below can replay unmodified on plain Neon
-- Postgres, which has no auth/extensions schemas, no `authenticated` role,
-- and no auth.uid()/auth.jwt() functions.
--
-- This is intentionally temporary: RLS stays on as defense-in-depth for
-- Phase 0, but the Go backend connects as a privileged role and owns real
-- authorization. Phase 4 (auth cutover) replaces auth.users with a
-- backend-owned users table and removes this shim.

CREATE SCHEMA IF NOT EXISTS auth;

CREATE TABLE IF NOT EXISTS auth.users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text,
    raw_user_meta_data jsonb DEFAULT '{}'::jsonb,
    created_at timestamptz DEFAULT now()
);

CREATE OR REPLACE FUNCTION auth.uid() RETURNS uuid
    LANGUAGE sql STABLE
    AS $$ SELECT NULLIF(current_setting('app.current_user_id', true), '')::uuid $$;

CREATE OR REPLACE FUNCTION auth.jwt() RETURNS jsonb
    LANGUAGE sql STABLE
    AS $$ SELECT COALESCE(NULLIF(current_setting('app.current_jwt_claims', true), ''), '{}')::jsonb $$;

CREATE SCHEMA IF NOT EXISTS extensions;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp" WITH SCHEMA extensions;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        CREATE ROLE authenticated NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon') THEN
        CREATE ROLE anon NOLOGIN;
    END IF;
END
$$;
