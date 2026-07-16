ALTER TABLE public.suppliers
    DROP COLUMN IF EXISTS contact,
    DROP COLUMN IF EXISTS terms;
