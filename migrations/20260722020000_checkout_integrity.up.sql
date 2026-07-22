-- Checkout integrity + in-store pickup support.
--
-- 1. READY_FOR_PICKUP order status for the click-and-collect flow.
--    (PG 12+ allows ADD VALUE inside a transaction as long as the new value
--    isn't used in the same transaction — nothing below references it.)
ALTER TYPE public.order_status ADD VALUE IF NOT EXISTS 'READY_FOR_PICKUP';

-- 2. Track whether an order's items have been deducted from product stock,
--    so the deduction (on payment / COD placement) and the restore (on
--    cancellation) are both idempotent.
ALTER TABLE public.orders
  ADD COLUMN IF NOT EXISTS stock_decremented boolean NOT NULL DEFAULT false;

-- 3. Expose delivery_method + shipping_fee on the checkout summary RPC so
--    the success page can render pickup orders correctly.
CREATE OR REPLACE FUNCTION public.get_checkout_order_summary(p_order_id uuid, p_email text)
RETURNS json
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
  result json;
  norm_email text := lower(trim(COALESCE(p_email, '')));
BEGIN
  IF norm_email = '' OR p_order_id IS NULL THEN
    RETURN NULL;
  END IF;

  SELECT json_build_object(
    'id', o.id,
    'payment_status', o.payment_status,
    'status', o.status::text,
    'total_amount', o.total_amount,
    'payment_method', o.payment_method,
    'delivery_method', o.delivery_method,
    'shipping_fee', o.shipping_fee,
    'shipping_address', o.shipping_address,
    'kra_pin', o.kra_pin,
    'items', COALESCE(
      (
        SELECT json_agg(
          json_build_object(
            'name', p.name,
            'quantity', oi.quantity,
            'unit_price', oi.unit_price
          )
          ORDER BY oi.id
        )
        FROM order_items oi
        JOIN products p ON p.id = oi.product_id
        WHERE oi.order_id = o.id
      ),
      '[]'::json
    ),
    'receipt_pdf_path', (
      SELECT ti.receipt_pdf_path
      FROM tax_invoices ti
      WHERE ti.order_id = o.id
      ORDER BY ti.created_at DESC NULLS LAST
      LIMIT 1
    )
  )
  INTO result
  FROM orders o
  WHERE o.id = p_order_id
    AND lower(trim(COALESCE(o.shipping_address->>'email', ''))) = norm_email;

  RETURN result;
END;
$$;
