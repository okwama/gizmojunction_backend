-- ============================================================
-- Shipping Zones & Fee System
-- ============================================================

-- 1. Create shipping_zones table
CREATE TABLE IF NOT EXISTS public.shipping_zones (
  id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
  name text NOT NULL,
  counties text[] NOT NULL DEFAULT '{}',
  standard_fee numeric NOT NULL DEFAULT 0,
  express_fee numeric NOT NULL DEFAULT 0,
  sort_order integer DEFAULT 0,
  is_active boolean DEFAULT true,
  created_at timestamptz DEFAULT now(),
  updated_at timestamptz DEFAULT now()
);

-- 2. Add shipping columns to orders
ALTER TABLE public.orders
  ADD COLUMN IF NOT EXISTS shipping_fee numeric NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS delivery_method text DEFAULT 'standard';

-- 3. Enable RLS on shipping_zones
ALTER TABLE public.shipping_zones ENABLE ROW LEVEL SECURITY;

-- Public read (needed for checkout fee calculation)
CREATE POLICY "Anyone can read active shipping zones"
  ON public.shipping_zones FOR SELECT
  USING (is_active = true);

-- Admin write
CREATE POLICY "Admins can manage shipping zones"
  ON public.shipping_zones FOR ALL
  USING (
    EXISTS (
      SELECT 1 FROM public.profiles
      WHERE id = auth.uid() AND role = 'ADMIN'
    )
  );

-- 4. Seed default shipping zones (all 47 Kenyan counties)
INSERT INTO public.shipping_zones (name, counties, standard_fee, express_fee, sort_order) VALUES
(
  'Nairobi',
  ARRAY['nairobi'],
  200, 400, 1
),
(
  'Nairobi Environs',
  ARRAY['kiambu', 'kajiado', 'machakos', 'murang''a'],
  350, 600, 2
),
(
  'Central Kenya',
  ARRAY['nakuru', 'nyeri', 'kirinyaga', 'nyandarua', 'laikipia', 'meru', 'embu', 'tharaka-nithi'],
  500, 900, 3
),
(
  'Rift Valley',
  ARRAY['uasin gishu', 'nandi', 'kericho', 'bomet', 'narok', 'trans-nzoia', 'baringo', 'elgeyo-marakwet', 'west pokot', 'samburu'],
  550, 1000, 4
),
(
  'Western Kenya',
  ARRAY['kakamega', 'vihiga', 'bungoma', 'busia'],
  600, 1100, 5
),
(
  'Nyanza',
  ARRAY['kisumu', 'siaya', 'kisii', 'nyamira', 'homa bay', 'migori'],
  600, 1100, 6
),
(
  'Eastern Kenya',
  ARRAY['kitui', 'makueni'],
  650, 1100, 7
),
(
  'Coast',
  ARRAY['mombasa', 'kwale', 'kilifi', 'taita-taveta', 'lamu', 'tana river'],
  700, 1200, 8
),
(
  'North Eastern',
  ARRAY['garissa', 'wajir', 'mandera', 'isiolo', 'marsabit', 'turkana'],
  900, 1500, 9
)
ON CONFLICT DO NOTHING;
