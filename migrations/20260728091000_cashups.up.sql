ALTER TABLE public.orders ADD COLUMN served_by uuid REFERENCES public.profiles(id);

CREATE TABLE public.cash_ups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cashier_id uuid NOT NULL REFERENCES public.profiles(id),
    period_start timestamptz NOT NULL,
    period_end timestamptz NOT NULL,
    expected_cash numeric(12,2) NOT NULL DEFAULT 0,
    counted_cash numeric(12,2) NOT NULL,
    variance numeric(12,2) NOT NULL,
    breakdown jsonb NOT NULL DEFAULT '{}'::jsonb,
    notes text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_cash_ups_cashier_id ON public.cash_ups(cashier_id);
