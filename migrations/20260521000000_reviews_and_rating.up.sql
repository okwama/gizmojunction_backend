-- The frontend reads products.rating, products.review_count, and a reviews
-- table that don't exist anywhere in the migrated schema (public_schema.sql
-- predates them, same drift pattern as the empty remote_schema.sql in
-- Phase 0). This is a best-guess definition inferred from how the frontend
-- uses these fields — validate against the real Supabase schema before
-- Phase 5 migrates review *submission*.

CREATE TABLE IF NOT EXISTS public.reviews (
    id uuid PRIMARY KEY DEFAULT extensions.uuid_generate_v4(),
    product_id uuid NOT NULL REFERENCES public.products(id) ON DELETE CASCADE,
    customer_id uuid REFERENCES public.profiles(id) ON DELETE SET NULL,
    author_name text,
    rating smallint NOT NULL CHECK (rating BETWEEN 1 AND 5),
    comment text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_reviews_product_id ON public.reviews (product_id);

ALTER TABLE public.products
    ADD COLUMN IF NOT EXISTS rating numeric(2,1) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS review_count integer NOT NULL DEFAULT 0;

CREATE OR REPLACE FUNCTION public.refresh_product_rating() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    affected_product_id uuid := COALESCE(NEW.product_id, OLD.product_id);
BEGIN
    UPDATE public.products SET
        rating = COALESCE((SELECT round(avg(rating)::numeric, 1) FROM public.reviews WHERE product_id = affected_product_id), 0),
        review_count = (SELECT count(*) FROM public.reviews WHERE product_id = affected_product_id)
    WHERE id = affected_product_id;
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS reviews_refresh_product_rating ON public.reviews;
CREATE TRIGGER reviews_refresh_product_rating
    AFTER INSERT OR UPDATE OR DELETE ON public.reviews
    FOR EACH ROW EXECUTE FUNCTION public.refresh_product_rating();
