-- store_category_id was INTEGER in the original supplier-sync schema, but
-- the admin UI has always sent categories.id — a uuid. The old /api service
-- typed it as *int too, so category mapping writes could never actually
-- store a valid reference. Fixed as part of folding /api into the Go
-- backend: both columns become real uuid references. Existing integer
-- values (if any) are meaningless against uuid category ids, so they are
-- dropped rather than cast.
ALTER TABLE supplier_category_mappings
    ALTER COLUMN store_category_id TYPE uuid USING NULL;
ALTER TABLE supplier_products
    ALTER COLUMN store_category_id TYPE uuid USING NULL;
