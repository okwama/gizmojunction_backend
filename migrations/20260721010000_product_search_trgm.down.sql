DROP INDEX IF EXISTS idx_products_sku_trgm;
DROP INDEX IF EXISTS idx_products_brand_trgm;
DROP INDEX IF EXISTS idx_products_name_trgm;
DROP EXTENSION IF EXISTS pg_trgm;
