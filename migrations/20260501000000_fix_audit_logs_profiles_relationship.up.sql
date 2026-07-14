-- Fix audit_logs relationship to profiles
-- This migration adds a foreign key constraint and ensures profiles have email for easier display

-- 1. Add email to profiles if it doesn't exist
DO $$ 
BEGIN 
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'profiles' AND column_name = 'email') THEN
        ALTER TABLE public.profiles ADD COLUMN email text;
    END IF;
END $$;

-- 2. Sync emails from auth.users to profiles
UPDATE public.profiles p
SET email = u.email
FROM auth.users u
WHERE p.id = u.id AND p.email IS NULL;

-- 3. Update the handle_new_user function to include email
CREATE OR REPLACE FUNCTION public.handle_new_user()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
BEGIN
  INSERT INTO public.profiles (id, full_name, phone, role, email)
  VALUES (
    new.id,
    new.raw_user_meta_data->>'full_name',
    new.raw_user_meta_data->>'phone',
    COALESCE(new.raw_user_meta_data->>'role', 'CUSTOMER')::public.user_role,
    new.email
  );
  RETURN new;
END;
$$;

-- 4. Add foreign key from audit_logs to profiles
-- First remove any existing broken FK to auth.users if we want to point to profiles
-- Actually, it's better to keep the FK to auth.users for data integrity, 
-- but PostgREST needs a FK to profiles to do the join as requested in code.
ALTER TABLE public.audit_logs
DROP CONSTRAINT IF EXISTS audit_logs_user_id_fkey;

ALTER TABLE public.audit_logs
ADD CONSTRAINT audit_logs_user_id_profiles_fkey 
FOREIGN KEY (user_id) REFERENCES public.profiles(id)
ON DELETE SET NULL;
