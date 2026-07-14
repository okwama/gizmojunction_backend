-- Add direct relationship between orders and profiles for easier joining
ALTER TABLE public.orders
DROP CONSTRAINT IF EXISTS orders_customer_id_fkey,
ADD CONSTRAINT orders_customer_id_fkey 
  FOREIGN KEY (customer_id) 
  REFERENCES public.profiles(id) 
  ON DELETE SET NULL;

-- Ensure RLS allows the join
ALTER TABLE public.orders FORCE ROW LEVEL SECURITY;
