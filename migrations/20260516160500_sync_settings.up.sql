CREATE TABLE sync_settings (
    key   VARCHAR(50) PRIMARY KEY,
    value JSONB       NOT NULL,
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Insert default markup
INSERT INTO sync_settings (key, value) VALUES ('default_markup', '60');
