-- ERP Reporting Updates: Historical Cost Capture
-- Run this in your Supabase SQL Editor

-- 1. Update order_items to store cost at time of sale
ALTER TABLE public.order_items 
ADD COLUMN IF NOT EXISTS cost_price NUMERIC(12,2) DEFAULT 0;

-- 2. Update orders to store aggregate cost and profit
ALTER TABLE public.orders 
ADD COLUMN IF NOT EXISTS total_cost NUMERIC(12,2) DEFAULT 0,
ADD COLUMN IF NOT EXISTS total_profit NUMERIC(12,2) DEFAULT 0;

-- 3. Update existing orders to have a total_profit for historical data (Best effort)
UPDATE public.orders
SET total_profit = total_amount - total_cost
WHERE total_profit = 0;

COMMENT ON COLUMN public.order_items.cost_price IS 'The supplier cost of the item at the time of order placement.';
COMMENT ON COLUMN public.orders.total_cost IS 'Sum of cost_price for all items in the order.';
COMMENT ON COLUMN public.orders.total_profit IS 'Total Revenue (total_amount) minus Total Cost.';
