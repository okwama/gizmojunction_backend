-- Enable RLS
ALTER TABLE supplier_products ENABLE ROW LEVEL SECURITY;
ALTER TABLE supplier_category_mappings ENABLE ROW LEVEL SECURITY;
ALTER TABLE sync_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE store_products ENABLE ROW LEVEL SECURITY;

-- Allow all for now (Assuming admin is authenticated)
-- In a real app, we'd check roles, but for this admin portal we'll allow authenticated
CREATE POLICY "Allow authenticated read products" ON supplier_products FOR SELECT TO authenticated USING (true);
CREATE POLICY "Allow authenticated read mappings" ON supplier_category_mappings FOR SELECT TO authenticated USING (true);
CREATE POLICY "Allow authenticated read sessions" ON sync_sessions FOR SELECT TO authenticated USING (true);
CREATE POLICY "Allow authenticated read store" ON store_products FOR SELECT TO authenticated USING (true);

-- Also allow anon read for local dev if needed, but authenticated is safer
CREATE POLICY "Allow anon read products" ON supplier_products FOR SELECT TO anon USING (true);
