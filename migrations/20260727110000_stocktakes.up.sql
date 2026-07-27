CREATE TABLE public.stock_takes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    category_id uuid REFERENCES public.categories(id),
    status text NOT NULL DEFAULT 'open',
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE TABLE public.stock_take_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stock_take_id uuid NOT NULL REFERENCES public.stock_takes(id) ON DELETE CASCADE,
    product_id uuid NOT NULL REFERENCES public.products(id),
    expected_quantity integer NOT NULL,
    counted_quantity integer,
    reason text
);

CREATE INDEX idx_stock_take_items_stock_take_id ON public.stock_take_items(stock_take_id);
