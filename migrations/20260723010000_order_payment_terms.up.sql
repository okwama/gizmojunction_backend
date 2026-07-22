-- Commercial/pro-forma invoices (distinct from the KRA eTIMS fiscal tax
-- invoice, which already exists) need a payment-terms line — B2B buyers
-- expect one ("Net 30", "Due on receipt", etc.) and iVend shows it too.
ALTER TABLE public.orders
  ADD COLUMN IF NOT EXISTS payment_terms text;
