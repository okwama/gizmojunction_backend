CREATE TABLE public.shifts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    cashier_id uuid NOT NULL REFERENCES public.profiles(id),
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at timestamptz,
    ended_reason text
);

CREATE INDEX idx_shifts_cashier_id ON public.shifts(cashier_id);

CREATE UNIQUE INDEX idx_shifts_one_open_per_cashier ON public.shifts(cashier_id) WHERE ended_at IS NULL;
