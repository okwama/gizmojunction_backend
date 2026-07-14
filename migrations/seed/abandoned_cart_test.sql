INSERT INTO auth.users (id, email) VALUES
  ('22222222-2222-2222-2222-222222222222', 'testcustomer@example.com')
ON CONFLICT DO NOTHING;

INSERT INTO profiles (id, full_name, email) VALUES
  ('22222222-2222-2222-2222-222222222222', 'Test Customer', 'testcustomer@example.com')
ON CONFLICT DO NOTHING;

INSERT INTO orders (id, customer_id, status, total_amount, created_at) VALUES
  ('33333333-3333-3333-3333-333333333333', '22222222-2222-2222-2222-222222222222', 'PENDING', 89999.00, now() - interval '3 hours')
ON CONFLICT DO NOTHING;

INSERT INTO order_items (order_id, product_id, quantity, unit_price) VALUES
  ('33333333-3333-3333-3333-333333333333', 'a0000000-0000-0000-0000-000000000001', 1, 89999.00)
ON CONFLICT DO NOTHING;
