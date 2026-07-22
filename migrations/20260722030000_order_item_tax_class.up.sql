-- Snapshot each line's tax classification at sale time, same rationale as
-- order_items.unit_price/cost_price: a product's tax_class can change later
-- (reclassified, or a data-entry fix), but the receipt for a past order must
-- keep showing the rate that actually applied at checkout.
ALTER TABLE public.order_items
  ADD COLUMN IF NOT EXISTS tax_class text DEFAULT 'VAT_16';

-- Best-effort backfill for orders placed before this column existed: assume
-- the product's current tax_class, which is the only information available
-- for historical rows (orphaned/deleted products keep the 'VAT_16' default).
UPDATE public.order_items oi
SET tax_class = p.tax_class
FROM public.products p
WHERE oi.product_id = p.id
  AND oi.tax_class IS DISTINCT FROM p.tax_class
  AND p.tax_class IS NOT NULL;
