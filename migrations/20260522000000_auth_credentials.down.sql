DROP TABLE IF EXISTS public.refresh_tokens;
ALTER TABLE public.profiles DROP COLUMN IF EXISTS password_hash;
ALTER TABLE public.profiles DROP COLUMN IF EXISTS password_algo;
