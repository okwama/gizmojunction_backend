DROP TRIGGER IF EXISTS reviews_refresh_product_rating ON public.reviews;
DROP FUNCTION IF EXISTS public.refresh_product_rating();
ALTER TABLE public.products DROP COLUMN IF EXISTS rating;
ALTER TABLE public.products DROP COLUMN IF EXISTS review_count;
DROP TABLE IF EXISTS public.reviews;
