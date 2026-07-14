-- Ad hoc test data for verifying Phase 1 catalog endpoints locally. Not
-- part of the real migration history.

UPDATE categories SET is_featured_on_home = true
WHERE id IN ('66676869-7071-4000-8000-000000000001', '66676869-7071-4000-8000-000000000002');

INSERT INTO brands (id, name, logo_url, slug, is_featured) VALUES
  ('11111111-1111-1111-1111-111111111111', 'Acme', 'https://example.com/acme-logo.png', 'acme', true)
ON CONFLICT DO NOTHING;

INSERT INTO products (id, name, brand, brand_id, sku, description, price, stock_quantity, category_id, image_url, is_published, is_featured) VALUES
  ('a0000000-0000-0000-0000-000000000001', 'Acme Laptop Pro 14', 'Acme', '11111111-1111-1111-1111-111111111111', 'SKU-LAPTOP-001', 'A solid laptop.', 89999.00, 12, '563bd4dd-4128-4859-817b-f37f562f1d8e', 'https://example.com/laptop.jpg', true, true),
  ('a0000000-0000-0000-0000-000000000002', 'Acme Laptop Air 13', 'Acme', '11111111-1111-1111-1111-111111111111', 'SKU-LAPTOP-002', 'A lighter laptop.', 74999.00, 5, '563bd4dd-4128-4859-817b-f37f562f1d8e', 'https://example.com/laptop2.jpg', true, false),
  ('a0000000-0000-0000-0000-000000000003', 'Acme Desktop Tower', 'Acme', '11111111-1111-1111-1111-111111111111', 'SKU-DESK-001', 'A desktop.', 65000.00, 3, 'dba9d7e0-f5b6-45ef-83e9-355b89627fc4', NULL, true, false),
  ('a0000000-0000-0000-0000-000000000004', 'Acme Smartphone X', 'Acme', '11111111-1111-1111-1111-111111111111', 'SKU-PHONE-001', 'A phone.', 45000.00, 20, 'd8ef57d0-7959-4f4c-a261-6b6983f2c442', 'https://example.com/phone.jpg', true, true)
ON CONFLICT DO NOTHING;

INSERT INTO reviews (product_id, author_name, rating, comment) VALUES
  ('a0000000-0000-0000-0000-000000000001', 'Jane', 5, 'Great laptop!'),
  ('a0000000-0000-0000-0000-000000000001', 'Bob', 4, 'Pretty good.');
