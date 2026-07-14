-- Add expo_push_token to profiles
ALTER TABLE public.profiles ADD COLUMN IF NOT EXISTS expo_push_token TEXT;
