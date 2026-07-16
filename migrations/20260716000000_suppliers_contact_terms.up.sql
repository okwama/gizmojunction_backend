-- suppliers.contact / suppliers.terms are read and written by the admin ERP
-- page against the live Supabase database, but are missing from the replayed
-- baseline dump — same schema-drift pattern as the reviews table (see
-- 20260521000000_reviews_and_rating). Validate against the live Supabase
-- definition before the ERP domain's final data re-sync.
ALTER TABLE public.suppliers
    ADD COLUMN IF NOT EXISTS contact text,
    ADD COLUMN IF NOT EXISTS terms text;
