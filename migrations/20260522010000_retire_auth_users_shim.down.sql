ALTER TABLE public.stock_movements
    DROP CONSTRAINT IF EXISTS stock_movements_created_by_fkey,
    ADD CONSTRAINT stock_movements_created_by_fkey FOREIGN KEY (created_by) REFERENCES auth.users(id);

ALTER TABLE public.blog_posts
    DROP CONSTRAINT IF EXISTS blog_posts_author_id_fkey,
    ADD CONSTRAINT blog_posts_author_id_fkey FOREIGN KEY (author_id) REFERENCES auth.users(id);

ALTER TABLE public.addresses
    DROP CONSTRAINT IF EXISTS addresses_user_id_fkey,
    ADD CONSTRAINT addresses_user_id_fkey FOREIGN KEY (user_id) REFERENCES auth.users(id) ON DELETE CASCADE;

ALTER TABLE public.profiles
    ADD CONSTRAINT profiles_id_fkey FOREIGN KEY (id) REFERENCES auth.users(id) ON DELETE CASCADE;
