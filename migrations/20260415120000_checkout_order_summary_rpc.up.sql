-- Allow guest checkout success page to load order status when billing email matches (RLS otherwise blocks anon SELECT on orders).

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
    'shipping_address', o.shipping_address,
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
    )
  )
  INTO result
  FROM orders o
  WHERE o.id = p_order_id
    AND lower(trim(COALESCE(o.shipping_address->>'email', ''))) = norm_email;

  RETURN result;
END;
$$;

REVOKE ALL ON FUNCTION public.get_checkout_order_summary(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.get_checkout_order_summary(uuid, text) TO anon;
GRANT EXECUTE ON FUNCTION public.get_checkout_order_summary(uuid, text) TO authenticated;
