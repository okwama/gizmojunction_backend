-- Faceted-search brand/department counts (backend/internal/catalog and
-- internal/search) run on every category/search page load now, grouping by
-- brand_id and parent_id — index both.
CREATE INDEX IF NOT EXISTS idx_products_brand_id ON public.products USING btree (brand_id);
CREATE INDEX IF NOT EXISTS idx_categories_parent_id ON public.categories USING btree (parent_id);
