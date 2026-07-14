-- Migration for KRA eTIMS OSCU Integration
-- Adds tax fields to products/orders and creates the tax_invoices table

-- 1. Update products table
ALTER TABLE public.products 
ADD COLUMN IF NOT EXISTS tax_class text DEFAULT 'VAT_16',
ADD COLUMN IF NOT EXISTS tax_rate numeric DEFAULT 16.0;

COMMENT ON COLUMN public.products.tax_class IS 'eTIMS tax category: VAT_16 (Standard), VAT_0 (Zero Rated), EXEMPT, etc.';

-- 2. Create tax_invoice_status enum if it doesn't exist
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'tax_invoice_status') THEN
        CREATE TYPE tax_invoice_status AS ENUM ('PENDING', 'ISSUED', 'FAILED', 'FAILED_FINAL');
    END IF;
END$$;

-- 3. Create tax_invoices table
CREATE TABLE IF NOT EXISTS public.tax_invoices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id uuid NOT NULL REFERENCES public.orders(id) UNIQUE,
    status tax_invoice_status DEFAULT 'PENDING',
    taxpayer_pin text NOT NULL,
    buyer_pin text,
    cu_id text,
    cu_invoice_number text,
    receipt_signature text,
    internal_data text,
    receipt_number text UNIQUE,
    total_amount numeric(12,2) NOT NULL,
    tax_amount numeric(12,2) NOT NULL,
    request_payload jsonb,
    response_payload jsonb,
    attempt_count integer DEFAULT 0,
    last_attempt_at timestamptz,
    issued_at timestamptz,
    error_message text,
    receipt_pdf_path text,
    created_at timestamptz DEFAULT now(),
    updated_at timestamptz DEFAULT now()
);

-- 4. Update orders table with tax tracking fields
ALTER TABLE public.orders
ADD COLUMN IF NOT EXISTS tax_status tax_invoice_status DEFAULT 'PENDING',
ADD COLUMN IF NOT EXISTS tax_invoice_id uuid REFERENCES public.tax_invoices(id),
ADD COLUMN IF NOT EXISTS tax_attempts integer DEFAULT 0,
ADD COLUMN IF NOT EXISTS tax_last_attempt_at timestamptz,
ADD COLUMN IF NOT EXISTS tax_error_message text;

-- 5. Enable RLS on tax_invoices
ALTER TABLE public.tax_invoices ENABLE ROW LEVEL SECURITY;

-- 6. Add RLS Policies
-- Only admins can see/manage tax invoices for now
DROP POLICY IF EXISTS "Admins have full access to tax_invoices" ON public.tax_invoices;
CREATE POLICY "Admins have full access to tax_invoices"
ON public.tax_invoices
FOR ALL
TO authenticated
USING (
  EXISTS (
    SELECT 1 FROM public.profiles
    WHERE profiles.id = auth.uid()
    AND profiles.role = 'ADMIN'
  )
);

-- Customers can view their own tax invoice metadata (read-only)
DROP POLICY IF EXISTS "Customers can view their own tax invoices" ON public.tax_invoices;
CREATE POLICY "Customers can view their own tax invoices"
ON public.tax_invoices
FOR SELECT
TO authenticated
USING (
  EXISTS (
    SELECT 1 FROM public.orders
    WHERE orders.id = tax_invoices.order_id
    AND orders.customer_id = auth.uid()
  )
);

-- 7. Add trigger for updated_at
CREATE OR REPLACE FUNCTION public.handle_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS set_tax_invoices_updated_at ON public.tax_invoices;
CREATE TRIGGER set_tax_invoices_updated_at
    BEFORE UPDATE ON public.tax_invoices
    FOR EACH ROW
    EXECUTE FUNCTION public.handle_updated_at();
