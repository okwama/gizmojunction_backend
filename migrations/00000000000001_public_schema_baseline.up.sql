--
-- PostgreSQL database dump
--


-- Dumped from database version 17.6
-- Dumped by pg_dump version 18.3

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: public; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA IF NOT EXISTS public;


--
-- Name: SCHEMA public; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON SCHEMA public IS 'standard public schema';


--
-- Name: order_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.order_status AS ENUM (
    'PENDING',
    'PROCESSING',
    'SHIPPED',
    'DELIVERED',
    'CANCELLED'
);


--
-- Name: po_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.po_status AS ENUM (
    'DRAFT',
    'SENT',
    'RECEIVED',
    'CANCELLED'
);


--
-- Name: stock_movement_type; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.stock_movement_type AS ENUM (
    'IN',
    'OUT',
    'ADJUSTMENT'
);


--
-- Name: tax_invoice_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.tax_invoice_status AS ENUM (
    'PENDING',
    'ISSUED',
    'FAILED',
    'FAILED_FINAL'
);


--
-- Name: user_role; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.user_role AS ENUM (
    'ADMIN',
    'CUSTOMER'
);


--
-- Name: audit_trigger_func(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.audit_trigger_func() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    AS $$
DECLARE
    target_user_id UUID;
BEGIN
    -- Try to get the current user ID from Supabase auth
    BEGIN
        target_user_id := auth.uid();
    EXCEPTION WHEN OTHERS THEN
        target_user_id := NULL;
    END;

    IF (TG_OP = 'INSERT') THEN
        INSERT INTO audit_logs (user_id, action, resource, metadata)
        VALUES (target_user_id, 'CREATE', TG_TABLE_NAME, row_to_json(NEW)::jsonb);
        RETURN NEW;
    ELSIF (TG_OP = 'UPDATE') THEN
        INSERT INTO audit_logs (user_id, action, resource, metadata)
        VALUES (target_user_id, 'UPDATE', TG_TABLE_NAME, jsonb_build_object('old', row_to_json(OLD)::jsonb, 'new', row_to_json(NEW)::jsonb));
        RETURN NEW;
    ELSIF (TG_OP = 'DELETE') THEN
        INSERT INTO audit_logs (user_id, action, resource, metadata)
        VALUES (target_user_id, 'DELETE', TG_TABLE_NAME, row_to_json(OLD)::jsonb);
        RETURN OLD;
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: get_checkout_order_summary(uuid, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_checkout_order_summary(p_order_id uuid, p_email text) RETURNS json
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public'
    AS $$
DECLARE
  result json;
  norm_email text := lower(trim(COALESCE(p_email, '')));
BEGIN
  IF norm_email = '' OR p_order_id IS NULL THEN
    RETURN NULL;
  END IF;

  SELECT json_build_object(
    'id', o.id,
    'payment_status', o.payment_status,
    'status', o.status::text,
    'total_amount', o.total_amount,
    'payment_method', o.payment_method,
    'shipping_address', o.shipping_address,
    'items', COALESCE(
      (
        SELECT json_agg(
          json_build_object(
            'name', p.name,
            'quantity', oi.quantity,
            'unit_price', oi.unit_price
          )
          ORDER BY oi.id
        )
        FROM order_items oi
        JOIN products p ON p.id = oi.product_id
        WHERE oi.order_id = o.id
      ),
      '[]'::json
    ),
    'receipt_pdf_path', (
      SELECT ti.receipt_pdf_path
      FROM tax_invoices ti
      WHERE ti.order_id = o.id
      ORDER BY ti.created_at DESC NULLS LAST
      LIMIT 1
    )
  )
  INTO result
  FROM orders o
  WHERE o.id = p_order_id
    AND lower(trim(COALESCE(o.shipping_address->>'email', ''))) = norm_email;

  RETURN result;
END;
$$;


--
-- Name: handle_new_user(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.handle_new_user() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    AS $$
BEGIN
  INSERT INTO public.profiles (id, full_name, phone, role)
  VALUES (
    new.id,
    new.raw_user_meta_data->>'full_name',
    new.raw_user_meta_data->>'phone',
    COALESCE(new.raw_user_meta_data->>'role', 'CUSTOMER')::public.user_role
  );
  RETURN new;
END;
$$;


--
-- Name: handle_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.handle_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;


--
-- Name: is_admin(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.is_admin() RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    AS $$
BEGIN
  RETURN EXISTS (
    SELECT 1 FROM public.profiles
    WHERE id = auth.uid()
    AND role = 'ADMIN'
  );
END;
$$;


--
-- Name: log_stock_movement(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.log_stock_movement() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF (OLD.stock_quantity IS DISTINCT FROM NEW.stock_quantity) THEN
        INSERT INTO public.stock_movements (product_id, movement_type, quantity, notes)
        VALUES (
            NEW.id, 
            'adjustment', 
            NEW.stock_quantity - OLD.stock_quantity, 
            'Automatic update from product edit'
        );
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: rls_auto_enable(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.rls_auto_enable() RETURNS event_trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog'
    AS $$
DECLARE
  cmd record;
BEGIN
  FOR cmd IN
    SELECT *
    FROM pg_event_trigger_ddl_commands()
    WHERE command_tag IN ('CREATE TABLE', 'CREATE TABLE AS', 'SELECT INTO')
      AND object_type IN ('table','partitioned table')
  LOOP
     IF cmd.schema_name IS NOT NULL AND cmd.schema_name IN ('public') AND cmd.schema_name NOT IN ('pg_catalog','information_schema') AND cmd.schema_name NOT LIKE 'pg_toast%' AND cmd.schema_name NOT LIKE 'pg_temp%' THEN
      BEGIN
        EXECUTE format('alter table if exists %s enable row level security', cmd.object_identity);
        RAISE LOG 'rls_auto_enable: enabled RLS on %', cmd.object_identity;
      EXCEPTION
        WHEN OTHERS THEN
          RAISE LOG 'rls_auto_enable: failed to enable RLS on %', cmd.object_identity;
      END;
     ELSE
        RAISE LOG 'rls_auto_enable: skip % (either system schema or not in enforced list: %.)', cmd.object_identity, cmd.schema_name;
     END IF;
  END LOOP;
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: addresses; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.addresses (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    user_id uuid,
    is_default boolean DEFAULT false,
    address_type text,
    full_name text NOT NULL,
    phone text,
    street_address text NOT NULL,
    apartment text,
    city text NOT NULL,
    county text,
    postal_code text,
    created_at timestamp with time zone DEFAULT now(),
    CONSTRAINT addresses_address_type_check CHECK ((address_type = ANY (ARRAY['SHIPPING'::text, 'BILLING'::text])))
);


--
-- Name: audit_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_logs (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    user_id uuid,
    action text NOT NULL,
    resource text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb,
    ip_address text,
    user_agent text,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: blog_posts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.blog_posts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    title character varying NOT NULL,
    slug character varying NOT NULL,
    excerpt text,
    content text,
    cover_image text,
    published_at timestamp with time zone DEFAULT timezone('utc'::text, now()),
    is_published boolean DEFAULT false,
    author_id uuid,
    created_at timestamp with time zone DEFAULT timezone('utc'::text, now()),
    updated_at timestamp with time zone DEFAULT timezone('utc'::text, now())
);


--
-- Name: brands; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.brands (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(255) NOT NULL,
    logo_url text,
    slug character varying(255),
    created_at timestamp with time zone DEFAULT now(),
    is_featured boolean DEFAULT false
);


--
-- Name: categories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.categories (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    description text,
    created_at timestamp with time zone DEFAULT now(),
    parent_id uuid,
    sort_order integer DEFAULT 0,
    is_visible boolean DEFAULT true,
    image_url text,
    is_featured_on_home boolean DEFAULT false
);


--
-- Name: import_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.import_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    filename text,
    status text DEFAULT 'pending'::text,
    total_rows integer DEFAULT 0,
    imported integer DEFAULT 0,
    errors integer DEFAULT 0,
    log jsonb DEFAULT '[]'::jsonb,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: inventory_ledger; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inventory_ledger (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    product_id uuid,
    movement_type public.stock_movement_type NOT NULL,
    quantity integer NOT NULL,
    reason text,
    reference_id text,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: order_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.order_items (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    order_id uuid,
    product_id uuid,
    quantity integer NOT NULL,
    unit_price numeric(12,2) NOT NULL,
    cost_price numeric(12,2) DEFAULT 0
);


--
-- Name: COLUMN order_items.cost_price; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.order_items.cost_price IS 'The supplier cost of the item at the time of order placement.';


--
-- Name: orders; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.orders (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    customer_id uuid,
    status public.order_status DEFAULT 'PENDING'::public.order_status,
    total_amount numeric(12,2) NOT NULL,
    shipping_address jsonb,
    payment_intent_id text,
    created_at timestamp with time zone DEFAULT now(),
    payment_method text,
    payment_status text DEFAULT 'unpaid'::text,
    payment_metadata jsonb DEFAULT '{}'::jsonb,
    total_cost numeric(12,2) DEFAULT 0,
    total_profit numeric(12,2) DEFAULT 0,
    tax_status public.tax_invoice_status DEFAULT 'PENDING'::public.tax_invoice_status,
    tax_invoice_id uuid,
    tax_attempts integer DEFAULT 0,
    tax_last_attempt_at timestamp with time zone,
    tax_error_message text,
    kra_pin text,
    loyalty_enrolled boolean DEFAULT false
);


--
-- Name: COLUMN orders.total_cost; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.orders.total_cost IS 'Sum of cost_price for all items in the order.';


--
-- Name: COLUMN orders.total_profit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.orders.total_profit IS 'Total Revenue (total_amount) minus Total Cost.';


--
-- Name: COLUMN orders.kra_pin; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.orders.kra_pin IS 'KRA PIN for tax invoicing purposes';


--
-- Name: COLUMN orders.loyalty_enrolled; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.orders.loyalty_enrolled IS 'Whether the customer opted into the loyalty program during this order';


--
-- Name: products; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.products (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    name text NOT NULL,
    brand text,
    sku text NOT NULL,
    description text,
    price numeric(12,2) NOT NULL,
    old_price numeric(12,2),
    stock_quantity integer DEFAULT 0,
    category_id uuid,
    image_url text,
    gallery text[],
    specifications jsonb,
    is_published boolean DEFAULT true,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    description_html text,
    description_plain text,
    summary_html text,
    summary_plain text,
    external_wc_id integer,
    import_job_id uuid,
    sale_price numeric(12,2) DEFAULT NULL::numeric,
    cost_price numeric(12,2) DEFAULT NULL::numeric,
    currency character(3) DEFAULT 'KES'::bpchar,
    low_stock_threshold integer DEFAULT 5,
    weight_kg numeric(8,3) DEFAULT NULL::numeric,
    barcode character varying(100) DEFAULT NULL::character varying,
    tags character varying[] DEFAULT '{}'::character varying[],
    video_url text,
    is_featured boolean DEFAULT false,
    brand_id uuid,
    tax_class text DEFAULT 'VAT_16'::text,
    tax_rate numeric DEFAULT 16.0
);


--
-- Name: COLUMN products.tax_class; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.products.tax_class IS 'eTIMS tax category: VAT_16 (Standard), VAT_0 (Zero Rated), EXEMPT, etc.';


--
-- Name: profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.profiles (
    id uuid NOT NULL,
    full_name text,
    phone text,
    role public.user_role DEFAULT 'CUSTOMER'::public.user_role,
    loyalty_points integer DEFAULT 0,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    is_active boolean DEFAULT true
);


--
-- Name: promotions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promotions (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    title text NOT NULL,
    description text,
    banner_url text,
    target_url text,
    is_active boolean DEFAULT true,
    starts_at timestamp with time zone,
    ends_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now(),
    display_location character varying DEFAULT 'hero'::character varying,
    badge_text character varying
);


--
-- Name: purchase_order_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.purchase_order_items (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    purchase_order_id uuid,
    product_id uuid,
    quantity integer NOT NULL,
    unit_cost numeric(12,2) NOT NULL
);


--
-- Name: purchase_orders; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.purchase_orders (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    supplier_id uuid,
    status public.po_status DEFAULT 'DRAFT'::public.po_status,
    total_amount numeric(12,2) DEFAULT 0,
    notes text,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: stock_movements; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.stock_movements (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    product_id uuid,
    movement_type text NOT NULL,
    quantity integer NOT NULL,
    reference_id uuid,
    notes text,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: store_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.store_settings (
    id integer NOT NULL,
    key character varying NOT NULL,
    value jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT timezone('utc'::text, now())
);


--
-- Name: store_settings_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.store_settings_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: store_settings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.store_settings_id_seq OWNED BY public.store_settings.id;


--
-- Name: suppliers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.suppliers (
    id uuid DEFAULT extensions.uuid_generate_v4() NOT NULL,
    name text NOT NULL,
    email text,
    phone text,
    address text,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: tax_invoices; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tax_invoices (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    status public.tax_invoice_status DEFAULT 'PENDING'::public.tax_invoice_status,
    taxpayer_pin text NOT NULL,
    buyer_pin text,
    cu_id text,
    cu_invoice_number text,
    receipt_signature text,
    internal_data text,
    receipt_number text,
    total_amount numeric(12,2) NOT NULL,
    tax_amount numeric(12,2) NOT NULL,
    request_payload jsonb,
    response_payload jsonb,
    attempt_count integer DEFAULT 0,
    last_attempt_at timestamp with time zone,
    issued_at timestamp with time zone,
    error_message text,
    receipt_pdf_path text,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: store_settings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.store_settings ALTER COLUMN id SET DEFAULT nextval('public.store_settings_id_seq'::regclass);


--
-- Name: addresses addresses_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.addresses
    ADD CONSTRAINT addresses_pkey PRIMARY KEY (id);


--
-- Name: audit_logs audit_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_logs
    ADD CONSTRAINT audit_logs_pkey PRIMARY KEY (id);


--
-- Name: blog_posts blog_posts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blog_posts
    ADD CONSTRAINT blog_posts_pkey PRIMARY KEY (id);


--
-- Name: blog_posts blog_posts_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blog_posts
    ADD CONSTRAINT blog_posts_slug_key UNIQUE (slug);


--
-- Name: brands brands_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brands
    ADD CONSTRAINT brands_name_key UNIQUE (name);


--
-- Name: brands brands_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brands
    ADD CONSTRAINT brands_pkey PRIMARY KEY (id);


--
-- Name: brands brands_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.brands
    ADD CONSTRAINT brands_slug_key UNIQUE (slug);


--
-- Name: categories categories_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_name_key UNIQUE (name);


--
-- Name: categories categories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_pkey PRIMARY KEY (id);


--
-- Name: categories categories_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_slug_key UNIQUE (slug);


--
-- Name: import_jobs import_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.import_jobs
    ADD CONSTRAINT import_jobs_pkey PRIMARY KEY (id);


--
-- Name: inventory_ledger inventory_ledger_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inventory_ledger
    ADD CONSTRAINT inventory_ledger_pkey PRIMARY KEY (id);


--
-- Name: order_items order_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.order_items
    ADD CONSTRAINT order_items_pkey PRIMARY KEY (id);


--
-- Name: orders orders_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_pkey PRIMARY KEY (id);


--
-- Name: products products_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.products
    ADD CONSTRAINT products_pkey PRIMARY KEY (id);


--
-- Name: products products_sku_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.products
    ADD CONSTRAINT products_sku_key UNIQUE (sku);


--
-- Name: profiles profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.profiles
    ADD CONSTRAINT profiles_pkey PRIMARY KEY (id);


--
-- Name: promotions promotions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotions
    ADD CONSTRAINT promotions_pkey PRIMARY KEY (id);


--
-- Name: purchase_order_items purchase_order_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.purchase_order_items
    ADD CONSTRAINT purchase_order_items_pkey PRIMARY KEY (id);


--
-- Name: purchase_orders purchase_orders_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.purchase_orders
    ADD CONSTRAINT purchase_orders_pkey PRIMARY KEY (id);


--
-- Name: stock_movements stock_movements_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stock_movements
    ADD CONSTRAINT stock_movements_pkey PRIMARY KEY (id);


--
-- Name: store_settings store_settings_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.store_settings
    ADD CONSTRAINT store_settings_key_key UNIQUE (key);


--
-- Name: store_settings store_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.store_settings
    ADD CONSTRAINT store_settings_pkey PRIMARY KEY (id);


--
-- Name: suppliers suppliers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.suppliers
    ADD CONSTRAINT suppliers_pkey PRIMARY KEY (id);


--
-- Name: tax_invoices tax_invoices_order_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tax_invoices
    ADD CONSTRAINT tax_invoices_order_id_key UNIQUE (order_id);


--
-- Name: tax_invoices tax_invoices_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tax_invoices
    ADD CONSTRAINT tax_invoices_pkey PRIMARY KEY (id);


--
-- Name: tax_invoices tax_invoices_receipt_number_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tax_invoices
    ADD CONSTRAINT tax_invoices_receipt_number_key UNIQUE (receipt_number);


--
-- Name: idx_audit_logs_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_logs_created_at ON public.audit_logs USING btree (created_at);


--
-- Name: idx_audit_logs_resource; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_logs_resource ON public.audit_logs USING btree (resource);


--
-- Name: idx_audit_logs_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_audit_logs_user_id ON public.audit_logs USING btree (user_id);


--
-- Name: idx_blog_posts_published; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_blog_posts_published ON public.blog_posts USING btree (published_at DESC) WHERE (is_published = true);


--
-- Name: idx_blog_posts_slug; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_blog_posts_slug ON public.blog_posts USING btree (slug);


--
-- Name: idx_ledger_product; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_ledger_product ON public.inventory_ledger USING btree (product_id);


--
-- Name: idx_orders_customer; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_orders_customer ON public.orders USING btree (customer_id);


--
-- Name: idx_orders_payment_intent_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_orders_payment_intent_id ON public.orders USING btree (payment_intent_id);


--
-- Name: idx_po_items_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_po_items_order ON public.purchase_order_items USING btree (purchase_order_id);


--
-- Name: idx_po_supplier; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_po_supplier ON public.purchase_orders USING btree (supplier_id);


--
-- Name: idx_products_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_products_category ON public.products USING btree (category_id);


--
-- Name: idx_products_description_plain; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_products_description_plain ON public.products USING gin (to_tsvector('english'::regconfig, description_plain));


--
-- Name: idx_products_sku; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_products_sku ON public.products USING btree (sku);


--
-- Name: idx_products_summary_plain; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_products_summary_plain ON public.products USING gin (to_tsvector('english'::regconfig, summary_plain));


--
-- Name: idx_stock_movements_product; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stock_movements_product ON public.stock_movements USING btree (product_id);


--
-- Name: idx_stock_movements_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stock_movements_type ON public.stock_movements USING btree (movement_type);


--
-- Name: orders audit_orders_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER audit_orders_trigger AFTER INSERT OR DELETE OR UPDATE ON public.orders FOR EACH ROW EXECUTE FUNCTION public.audit_trigger_func();


--
-- Name: products audit_products_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER audit_products_trigger AFTER INSERT OR DELETE OR UPDATE ON public.products FOR EACH ROW EXECUTE FUNCTION public.audit_trigger_func();


--
-- Name: profiles audit_profiles_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER audit_profiles_trigger AFTER INSERT OR DELETE OR UPDATE ON public.profiles FOR EACH ROW EXECUTE FUNCTION public.audit_trigger_func();


-- order-shipped-notification / orders_insert_order_notification triggers
-- omitted: they called supabase_functions.http_request(...) to invoke
-- Supabase Edge Functions directly from Postgres. Neon has no
-- supabase_functions extension, and per the migration plan (Phase 2) this
-- logic is reimplemented as river jobs enqueued from the Go backend after
-- an order write, not as a DB trigger calling out over HTTP.


--
-- Name: tax_invoices set_tax_invoices_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER set_tax_invoices_updated_at BEFORE UPDATE ON public.tax_invoices FOR EACH ROW EXECUTE FUNCTION public.handle_updated_at();


--
-- Name: products trigger_log_stock_on_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trigger_log_stock_on_update AFTER UPDATE OF stock_quantity ON public.products FOR EACH ROW EXECUTE FUNCTION public.log_stock_movement();


--
-- Name: addresses addresses_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.addresses
    ADD CONSTRAINT addresses_user_id_fkey FOREIGN KEY (user_id) REFERENCES auth.users(id) ON DELETE CASCADE;


--
-- Name: audit_logs audit_logs_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_logs
    ADD CONSTRAINT audit_logs_user_id_fkey FOREIGN KEY (user_id) REFERENCES auth.users(id);


--
-- Name: blog_posts blog_posts_author_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.blog_posts
    ADD CONSTRAINT blog_posts_author_id_fkey FOREIGN KEY (author_id) REFERENCES auth.users(id);


--
-- Name: categories categories_parent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.categories(id) ON DELETE CASCADE;


--
-- Name: inventory_ledger inventory_ledger_product_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inventory_ledger
    ADD CONSTRAINT inventory_ledger_product_id_fkey FOREIGN KEY (product_id) REFERENCES public.products(id) ON DELETE CASCADE;


--
-- Name: order_items order_items_order_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.order_items
    ADD CONSTRAINT order_items_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE CASCADE;


--
-- Name: order_items order_items_product_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.order_items
    ADD CONSTRAINT order_items_product_id_fkey FOREIGN KEY (product_id) REFERENCES public.products(id);


--
-- Name: orders orders_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES auth.users(id) ON DELETE SET NULL;


--
-- Name: orders orders_tax_invoice_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_tax_invoice_id_fkey FOREIGN KEY (tax_invoice_id) REFERENCES public.tax_invoices(id);


--
-- Name: products products_brand_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.products
    ADD CONSTRAINT products_brand_id_fkey FOREIGN KEY (brand_id) REFERENCES public.brands(id) ON DELETE SET NULL;


--
-- Name: products products_category_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.products
    ADD CONSTRAINT products_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.categories(id) ON DELETE SET NULL;


--
-- Name: products products_import_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.products
    ADD CONSTRAINT products_import_job_id_fkey FOREIGN KEY (import_job_id) REFERENCES public.import_jobs(id);


--
-- Name: profiles profiles_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.profiles
    ADD CONSTRAINT profiles_id_fkey FOREIGN KEY (id) REFERENCES auth.users(id) ON DELETE CASCADE;


--
-- Name: purchase_order_items purchase_order_items_product_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.purchase_order_items
    ADD CONSTRAINT purchase_order_items_product_id_fkey FOREIGN KEY (product_id) REFERENCES public.products(id) ON DELETE SET NULL;


--
-- Name: purchase_order_items purchase_order_items_purchase_order_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.purchase_order_items
    ADD CONSTRAINT purchase_order_items_purchase_order_id_fkey FOREIGN KEY (purchase_order_id) REFERENCES public.purchase_orders(id) ON DELETE CASCADE;


--
-- Name: purchase_orders purchase_orders_supplier_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.purchase_orders
    ADD CONSTRAINT purchase_orders_supplier_id_fkey FOREIGN KEY (supplier_id) REFERENCES public.suppliers(id) ON DELETE SET NULL;


--
-- Name: stock_movements stock_movements_created_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stock_movements
    ADD CONSTRAINT stock_movements_created_by_fkey FOREIGN KEY (created_by) REFERENCES auth.users(id);


--
-- Name: stock_movements stock_movements_product_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stock_movements
    ADD CONSTRAINT stock_movements_product_id_fkey FOREIGN KEY (product_id) REFERENCES public.products(id) ON DELETE CASCADE;


--
-- Name: tax_invoices tax_invoices_order_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tax_invoices
    ADD CONSTRAINT tax_invoices_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id);


--
-- Name: brands Admin All Access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admin All Access" ON public.brands TO authenticated USING (true) WITH CHECK (true);


--
-- Name: categories Admin All Access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admin All Access" ON public.categories TO authenticated USING (true) WITH CHECK (true);


--
-- Name: import_jobs Admin All Access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admin All Access" ON public.import_jobs TO authenticated USING (true) WITH CHECK (true);


--
-- Name: stock_movements Admin All Access; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admin All Access" ON public.stock_movements TO authenticated USING (true) WITH CHECK (true);


--
-- Name: brands Admins can delete brands; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can delete brands" ON public.brands FOR DELETE USING (public.is_admin());


--
-- Name: categories Admins can delete categories; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can delete categories" ON public.categories FOR DELETE USING (public.is_admin());


--
-- Name: blog_posts Admins can delete posts; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can delete posts" ON public.blog_posts FOR DELETE TO authenticated USING (true);


--
-- Name: products Admins can delete products; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can delete products" ON public.products FOR DELETE USING (public.is_admin());


--
-- Name: promotions Admins can delete promotions; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can delete promotions" ON public.promotions FOR DELETE TO authenticated USING (true);


--
-- Name: store_settings Admins can delete store settings; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can delete store settings" ON public.store_settings FOR DELETE TO authenticated USING (true);


--
-- Name: brands Admins can insert brands; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can insert brands" ON public.brands FOR INSERT WITH CHECK (public.is_admin());


--
-- Name: categories Admins can insert categories; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can insert categories" ON public.categories FOR INSERT WITH CHECK (public.is_admin());


--
-- Name: blog_posts Admins can insert posts; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can insert posts" ON public.blog_posts FOR INSERT TO authenticated WITH CHECK (true);


--
-- Name: products Admins can insert products; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can insert products" ON public.products FOR INSERT WITH CHECK (public.is_admin());


--
-- Name: promotions Admins can insert promotions; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can insert promotions" ON public.promotions FOR INSERT TO authenticated WITH CHECK (true);


--
-- Name: store_settings Admins can insert store settings; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can insert store settings" ON public.store_settings FOR INSERT TO authenticated WITH CHECK (true);


--
-- Name: orders Admins can update all orders; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can update all orders" ON public.orders FOR UPDATE USING (public.is_admin());


--
-- Name: profiles Admins can update all profiles; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can update all profiles" ON public.profiles FOR UPDATE USING (public.is_admin());


--
-- Name: brands Admins can update brands; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can update brands" ON public.brands FOR UPDATE USING (public.is_admin());


--
-- Name: categories Admins can update categories; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can update categories" ON public.categories FOR UPDATE USING (public.is_admin());


--
-- Name: blog_posts Admins can update posts; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can update posts" ON public.blog_posts FOR UPDATE TO authenticated USING (true);


--
-- Name: products Admins can update products; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can update products" ON public.products FOR UPDATE USING (public.is_admin());


--
-- Name: promotions Admins can update promotions; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can update promotions" ON public.promotions FOR UPDATE TO authenticated USING (true);


--
-- Name: store_settings Admins can update store settings; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can update store settings" ON public.store_settings FOR UPDATE TO authenticated USING (true);


--
-- Name: order_items Admins can view all order items; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can view all order items" ON public.order_items FOR SELECT USING (public.is_admin());


--
-- Name: orders Admins can view all orders; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can view all orders" ON public.orders FOR SELECT USING (public.is_admin());


--
-- Name: blog_posts Admins can view all posts; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can view all posts" ON public.blog_posts FOR SELECT TO authenticated USING (true);


--
-- Name: profiles Admins can view all profiles; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can view all profiles" ON public.profiles FOR SELECT USING (public.is_admin());


--
-- Name: audit_logs Admins can view audit logs; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins can view audit logs" ON public.audit_logs FOR SELECT USING ((EXISTS ( SELECT 1
   FROM public.profiles
  WHERE ((profiles.id = auth.uid()) AND (profiles.role = 'ADMIN'::public.user_role)))));


--
-- Name: categories Admins have full access to categories; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins have full access to categories" ON public.categories TO authenticated USING (((auth.jwt() ->> 'email'::text) ~~ '%@gizmojunction.com'::text));


--
-- Name: inventory_ledger Admins have full access to inventory; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins have full access to inventory" ON public.inventory_ledger TO authenticated USING (((auth.jwt() ->> 'email'::text) ~~ '%@gizmojunction.com'::text));


--
-- Name: order_items Admins have full access to order items; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins have full access to order items" ON public.order_items TO authenticated USING (((auth.jwt() ->> 'email'::text) ~~ '%@gizmojunction.com'::text)) WITH CHECK (((auth.jwt() ->> 'email'::text) ~~ '%@gizmojunction.com'::text));


--
-- Name: orders Admins have full access to orders; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins have full access to orders" ON public.orders TO authenticated USING (((auth.jwt() ->> 'email'::text) ~~ '%@gizmojunction.com'::text)) WITH CHECK (((auth.jwt() ->> 'email'::text) ~~ '%@gizmojunction.com'::text));


--
-- Name: products Admins have full access to products; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins have full access to products" ON public.products TO authenticated USING (((auth.jwt() ->> 'email'::text) ~~ '%@gizmojunction.com'::text));


--
-- Name: tax_invoices Admins have full access to tax_invoices; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Admins have full access to tax_invoices" ON public.tax_invoices TO authenticated USING ((EXISTS ( SELECT 1
   FROM public.profiles
  WHERE ((profiles.id = auth.uid()) AND (profiles.role = 'ADMIN'::public.user_role)))));


--
-- Name: order_items Anyone can insert order items; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Anyone can insert order items" ON public.order_items FOR INSERT TO authenticated, anon WITH CHECK (true);


--
-- Name: orders Anyone can insert orders; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Anyone can insert orders" ON public.orders FOR INSERT TO authenticated, anon WITH CHECK (true);


--
-- Name: order_items Anyone can view their own guest order items; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Anyone can view their own guest order items" ON public.order_items FOR SELECT TO authenticated, anon USING (true);


--
-- Name: orders Anyone can view their own guest orders; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Anyone can view their own guest orders" ON public.orders FOR SELECT TO authenticated, anon USING (true);


--
-- Name: tax_invoices Customers can view their own tax invoices; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Customers can view their own tax invoices" ON public.tax_invoices FOR SELECT TO authenticated USING ((EXISTS ( SELECT 1
   FROM public.orders
  WHERE ((orders.id = tax_invoices.order_id) AND (orders.customer_id = auth.uid())))));


--
-- Name: brands Public Read Brands; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public Read Brands" ON public.brands FOR SELECT TO authenticated, anon USING (true);


--
-- Name: categories Public Read Categories; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public Read Categories" ON public.categories FOR SELECT TO authenticated, anon USING (true);


--
-- Name: brands Public can view brands; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public can view brands" ON public.brands FOR SELECT USING (true);


--
-- Name: categories Public can view categories; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public can view categories" ON public.categories FOR SELECT USING (true);


--
-- Name: products Public can view products; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public can view products" ON public.products FOR SELECT USING (true);


--
-- Name: promotions Public can view promotions; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public can view promotions" ON public.promotions FOR SELECT USING (true);


--
-- Name: blog_posts Public can view published posts; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public can view published posts" ON public.blog_posts FOR SELECT USING ((is_published = true));


--
-- Name: store_settings Public can view store settings; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public can view store settings" ON public.store_settings FOR SELECT USING (true);


--
-- Name: categories Public categories are viewable by everyone; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public categories are viewable by everyone" ON public.categories FOR SELECT USING (true);


--
-- Name: products Public products are viewable by everyone; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public products are viewable by everyone" ON public.products FOR SELECT USING ((is_published = true));


--
-- Name: promotions Public promotions are viewable by everyone; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Public promotions are viewable by everyone" ON public.promotions FOR SELECT USING ((is_active = true));


--
-- Name: audit_logs Service role can insert audit logs; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Service role can insert audit logs" ON public.audit_logs FOR INSERT WITH CHECK (true);


--
-- Name: addresses Users can manage their own addresses; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Users can manage their own addresses" ON public.addresses USING ((auth.uid() = user_id));


--
-- Name: profiles Users can only see their own profile; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Users can only see their own profile" ON public.profiles FOR SELECT USING ((auth.uid() = id));


--
-- Name: profiles Users can update own profile; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Users can update own profile" ON public.profiles FOR UPDATE USING ((auth.uid() = id));


--
-- Name: orders Users can update their own orders; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Users can update their own orders" ON public.orders FOR UPDATE TO authenticated USING ((auth.uid() = customer_id)) WITH CHECK ((auth.uid() = customer_id));


--
-- Name: profiles Users can update their own profiles; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Users can update their own profiles" ON public.profiles FOR UPDATE USING ((auth.uid() = id));


--
-- Name: order_items Users can view own order items; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Users can view own order items" ON public.order_items FOR SELECT USING ((EXISTS ( SELECT 1
   FROM public.orders
  WHERE ((orders.id = order_items.order_id) AND (orders.customer_id = auth.uid())))));


--
-- Name: orders Users can view own orders; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Users can view own orders" ON public.orders FOR SELECT USING ((auth.uid() = customer_id));


--
-- Name: profiles Users can view own profile; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Users can view own profile" ON public.profiles FOR SELECT USING ((auth.uid() = id));


--
-- Name: order_items Users can view their own order items; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Users can view their own order items" ON public.order_items FOR SELECT USING ((EXISTS ( SELECT 1
   FROM public.orders
  WHERE ((orders.id = order_items.order_id) AND (orders.customer_id = auth.uid())))));


--
-- Name: orders Users can view their own orders; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY "Users can view their own orders" ON public.orders FOR SELECT USING ((auth.uid() = customer_id));


--
-- Name: addresses; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.addresses ENABLE ROW LEVEL SECURITY;

--
-- Name: audit_logs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.audit_logs ENABLE ROW LEVEL SECURITY;

--
-- Name: blog_posts; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.blog_posts ENABLE ROW LEVEL SECURITY;

--
-- Name: brands; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.brands ENABLE ROW LEVEL SECURITY;

--
-- Name: categories; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.categories ENABLE ROW LEVEL SECURITY;

--
-- Name: import_jobs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.import_jobs ENABLE ROW LEVEL SECURITY;

--
-- Name: inventory_ledger; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.inventory_ledger ENABLE ROW LEVEL SECURITY;

--
-- Name: order_items; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.order_items ENABLE ROW LEVEL SECURITY;

--
-- Name: orders; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.orders ENABLE ROW LEVEL SECURITY;

--
-- Name: products; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.products ENABLE ROW LEVEL SECURITY;

--
-- Name: profiles; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.profiles ENABLE ROW LEVEL SECURITY;

--
-- Name: promotions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.promotions ENABLE ROW LEVEL SECURITY;

--
-- Name: purchase_order_items; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.purchase_order_items ENABLE ROW LEVEL SECURITY;

--
-- Name: purchase_orders; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.purchase_orders ENABLE ROW LEVEL SECURITY;

--
-- Name: stock_movements; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.stock_movements ENABLE ROW LEVEL SECURITY;

--
-- Name: store_settings; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.store_settings ENABLE ROW LEVEL SECURITY;

--
-- Name: suppliers; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.suppliers ENABLE ROW LEVEL SECURITY;

--
-- Name: tax_invoices; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tax_invoices ENABLE ROW LEVEL SECURITY;

--
-- PostgreSQL database dump complete
--


