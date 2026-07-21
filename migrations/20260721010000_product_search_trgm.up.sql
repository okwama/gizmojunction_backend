-- Native Postgres product search: trigram indexes for typo-tolerant
-- name/brand/sku matching, complementing the description_plain/
-- summary_plain full-text GIN indexes already in the baseline schema.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS idx_products_name_trgm ON public.products USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_products_brand_trgm ON public.products USING gin (brand gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_products_sku_trgm ON public.products USING gin (sku gin_trgm_ops);
