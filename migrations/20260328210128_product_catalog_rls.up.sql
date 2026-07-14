-- Enable RLS on products, categories, and brands tables
ALTER TABLE public.products ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.categories ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.brands ENABLE ROW LEVEL SECURITY;

-- Cleanup existing policies if any to avoid conflicts
DROP POLICY IF EXISTS "Public can view products" ON public.products;
DROP POLICY IF EXISTS "Admins can insert products" ON public.products;
DROP POLICY IF EXISTS "Admins can update products" ON public.products;
DROP POLICY IF EXISTS "Admins can delete products" ON public.products;

DROP POLICY IF EXISTS "Public can view categories" ON public.categories;
DROP POLICY IF EXISTS "Admins can insert categories" ON public.categories;
DROP POLICY IF EXISTS "Admins can update categories" ON public.categories;
DROP POLICY IF EXISTS "Admins can delete categories" ON public.categories;

DROP POLICY IF EXISTS "Public can view brands" ON public.brands;
DROP POLICY IF EXISTS "Admins can insert brands" ON public.brands;
DROP POLICY IF EXISTS "Admins can update brands" ON public.brands;
DROP POLICY IF EXISTS "Admins can delete brands" ON public.brands;


-- Policies for public.products
CREATE POLICY "Public can view products" 
ON public.products 
FOR SELECT 
USING (true);

CREATE POLICY "Admins can insert products" 
ON public.products 
FOR INSERT 
WITH CHECK (public.is_admin());

CREATE POLICY "Admins can update products" 
ON public.products 
FOR UPDATE 
USING (public.is_admin());

CREATE POLICY "Admins can delete products" 
ON public.products 
FOR DELETE 
USING (public.is_admin());


-- Policies for public.categories
CREATE POLICY "Public can view categories" 
ON public.categories 
FOR SELECT 
USING (true);

CREATE POLICY "Admins can insert categories" 
ON public.categories 
FOR INSERT 
WITH CHECK (public.is_admin());

CREATE POLICY "Admins can update categories" 
ON public.categories 
FOR UPDATE 
USING (public.is_admin());

CREATE POLICY "Admins can delete categories" 
ON public.categories 
FOR DELETE 
USING (public.is_admin());


-- Policies for public.brands
CREATE POLICY "Public can view brands" 
ON public.brands 
FOR SELECT 
USING (true);

CREATE POLICY "Admins can insert brands" 
ON public.brands 
FOR INSERT 
WITH CHECK (public.is_admin());

CREATE POLICY "Admins can update brands" 
ON public.brands 
FOR UPDATE 
USING (public.is_admin());

CREATE POLICY "Admins can delete brands" 
ON public.brands 
FOR DELETE 
USING (public.is_admin());
