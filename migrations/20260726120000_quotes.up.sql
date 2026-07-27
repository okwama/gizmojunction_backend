-- Quotes: quote -> PDF -> convert-to-order (ADMIN_RETAIL_OPS_REVIEW.md §3).
-- Line items are their own table (quote_items), not a jsonb blob, matching
-- every other line-item domain in this schema (order_items, return_items,
-- purchase_order_items) rather than the doc's rough jsonb sketch. status is
-- a plain text column (draft|sent|accepted|expired|converted), consistent
-- with delivery_method/payment_method/refund_method — no enum, no
-- migration friction to add a value later.
CREATE TABLE public.quotes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_name text NOT NULL,
    customer_email text,
    customer_phone text,
    subtotal numeric(12,2) NOT NULL DEFAULT 0,
    vat_amount numeric(12,2) NOT NULL DEFAULT 0,
    total_amount numeric(12,2) NOT NULL DEFAULT 0,
    valid_until date,
    status text NOT NULL DEFAULT 'draft',
    notes text,
    converted_order_id uuid REFERENCES public.orders(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON COLUMN public.quotes.status IS 'draft | sent | accepted | expired | converted — converted is terminal (converted_order_id set).';

-- product_id is nullable and name/sku/tax_class are snapshotted (not
-- joined live) so a quote keeps showing what was actually quoted even if
-- the product is later renamed, reclassified, or deleted — same rationale
-- as order_items.
CREATE TABLE public.quote_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    quote_id uuid NOT NULL REFERENCES public.quotes(id) ON DELETE CASCADE,
    product_id uuid REFERENCES public.products(id) ON DELETE SET NULL,
    name text NOT NULL,
    sku text,
    quantity integer NOT NULL CHECK (quantity > 0),
    unit_price numeric(12,2) NOT NULL,
    tax_class text NOT NULL DEFAULT 'VAT_16'
);

CREATE INDEX idx_quotes_status ON public.quotes(status);
CREATE INDEX idx_quote_items_quote_id ON public.quote_items(quote_id);
