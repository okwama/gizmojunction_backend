-- Add is_active column to profiles table
ALTER TABLE public.profiles ADD COLUMN IF NOT EXISTS is_active BOOLEAN DEFAULT true;

-- Update existing profiles to be active
UPDATE public.profiles SET is_active = true WHERE is_active IS NULL;
