ALTER TABLE supplier_category_mappings
    ALTER COLUMN store_category_id TYPE integer USING NULL;
ALTER TABLE supplier_products
    ALTER COLUMN store_category_id TYPE integer USING NULL;
