-- Returns/refunds/exchanges (ADMIN_RETAIL_OPS_REVIEW.md §8.2). Purely
-- additive: no changes to order_status, tax_invoices, order_items, or the
-- stock_decremented flag. A return is a fact about an order, not a
-- replacement terminal status, and gets its own independent per-line stock
-- adjustment rather than reusing the all-or-nothing RestoreStock/
-- DecrementStock pair.

CREATE TABLE public.returns (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id uuid NOT NULL REFERENCES public.orders(id),
    reason text,
    refund_method text NOT NULL,
    refund_amount numeric(12,2) NOT NULL DEFAULT 0,
    refund_reference text,
    original_cu_invoice_number text,
    credit_note_status text NOT NULL DEFAULT 'not_applicable',
    credit_note_reference text,
    created_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON COLUMN public.returns.refund_method IS 'cash | mpesa_manual | card_manual | none — payout happens outside this system; this is a record, not an integration.';
COMMENT ON COLUMN public.returns.credit_note_status IS 'not_applicable | required | issued — required when the original order had an ISSUED KRA tax invoice; issued once an admin manually confirms it has been filed.';

CREATE TABLE public.return_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    return_id uuid NOT NULL REFERENCES public.returns(id) ON DELETE CASCADE,
    order_item_id uuid NOT NULL REFERENCES public.order_items(id),
    quantity integer NOT NULL CHECK (quantity > 0),
    condition text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON COLUMN public.return_items.condition IS 'restock | damaged — restock increments products.stock_quantity, damaged is a write-off.';

CREATE INDEX idx_returns_order_id ON public.returns(order_id);
CREATE INDEX idx_return_items_return_id ON public.return_items(return_id);
CREATE INDEX idx_return_items_order_item_id ON public.return_items(order_item_id);
