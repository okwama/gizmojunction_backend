CREATE TABLE supplier_products (
  id                SERIAL PRIMARY KEY,
  part_no           VARCHAR(50)   NOT NULL UNIQUE,
  brand             VARCHAR(50)   NOT NULL,
  sheet_name        VARCHAR(100)  NOT NULL,
  supplier_category VARCHAR(200)  NOT NULL,
  description       TEXT          NOT NULL,
  availability      VARCHAR(50)   NOT NULL,
  supplier_price    INTEGER       NOT NULL,
  source_version    VARCHAR(100),
  last_seen_at      TIMESTAMP     NOT NULL DEFAULT NOW(),
  created_at        TIMESTAMP     NOT NULL DEFAULT NOW(),
  updated_at        TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_supplier_products_part_no ON supplier_products(part_no);
CREATE INDEX idx_supplier_products_sheet ON supplier_products(sheet_name);
CREATE INDEX idx_supplier_products_category ON supplier_products(supplier_category);

CREATE TABLE supplier_price_history (
  id                    SERIAL PRIMARY KEY,
  part_no               VARCHAR(50)   NOT NULL,
  old_price             INTEGER,
  new_price             INTEGER       NOT NULL,
  old_availability      VARCHAR(50),
  new_availability      VARCHAR(50),
  source_version        VARCHAR(100),
  changed_at            TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_price_history_part_no ON supplier_price_history(part_no);
CREATE INDEX idx_price_history_changed_at ON supplier_price_history(changed_at);

CREATE TABLE store_products (
  id                SERIAL PRIMARY KEY,
  part_no           VARCHAR(50)   REFERENCES supplier_products(part_no),
  store_sku         VARCHAR(100)  UNIQUE,
  store_name        TEXT,
  store_price       INTEGER       NOT NULL,
  is_listed         BOOLEAN       DEFAULT TRUE,
  needs_review      BOOLEAN       DEFAULT FALSE,
  last_synced_at    TIMESTAMP,
  created_at        TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_store_products_part_no ON store_products(part_no);
CREATE INDEX idx_store_products_listed ON store_products(part_no, is_listed);

CREATE TABLE supplier_category_mappings (
  id                    SERIAL PRIMARY KEY,
  supplier_sheet        VARCHAR(100)  NOT NULL,
  supplier_category     VARCHAR(200)  NOT NULL,
  store_category_id     INTEGER       NULL,
  store_category_name   VARCHAR(200)  NULL,
  is_ignored            BOOLEAN       DEFAULT FALSE,
  mapped_at             TIMESTAMP,
  created_at            TIMESTAMP     DEFAULT NOW(),
  UNIQUE(supplier_sheet, supplier_category)
);

CREATE INDEX idx_category_mappings_category ON supplier_category_mappings(supplier_category);

CREATE TABLE sync_sessions (
  id                UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  source_version    VARCHAR(100),
  status            VARCHAR(20)   DEFAULT 'pending',
  diff_report       JSONB         NOT NULL,
  created_at        TIMESTAMP     DEFAULT NOW(),
  applied_at        TIMESTAMP
);

CREATE UNIQUE INDEX idx_single_pending_session 
ON sync_sessions (status) 
WHERE status = 'pending';
