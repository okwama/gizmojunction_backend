UPDATE orders
SET shipping_address = '{"first_name":"Test","last_name":"Customer","address":"123 Test St","city":"Nairobi","county":"Nairobi","phone":"0700000000","email":"testcustomer@example.com"}'::jsonb,
    payment_status = 'paid',
    payment_method = 'mpesa'
WHERE id = '33333333-3333-3333-3333-333333333333';
