-- Phase 4: profiles is now the canonical user table (the Go backend
-- creates rows here directly on signup — see internal/auth). Repoint the
-- remaining FKs that still reference the Phase 0 auth.users compat shim
-- so profile creation isn't blocked on inserting into that shim table.
-- orders.customer_id and audit_logs.user_id were already repointed to
-- profiles by earlier migrations; these four were not.

ALTER TABLE public.profiles
    DROP CONSTRAINT IF EXISTS profiles_id_fkey;

ALTER TABLE public.addresses
    DROP CONSTRAINT IF EXISTS addresses_user_id_fkey,
    ADD CONSTRAINT addresses_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.profiles(id) ON DELETE CASCADE;

ALTER TABLE public.blog_posts
    DROP CONSTRAINT IF EXISTS blog_posts_author_id_fkey,
    ADD CONSTRAINT blog_posts_author_id_fkey FOREIGN KEY (author_id) REFERENCES public.profiles(id);

ALTER TABLE public.stock_movements
    DROP CONSTRAINT IF EXISTS stock_movements_created_by_fkey,
    ADD CONSTRAINT stock_movements_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.profiles(id);
