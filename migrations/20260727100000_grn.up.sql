ALTER TABLE public.purchase_order_items
    ADD COLUMN received_quantity integer NOT NULL DEFAULT 0;
