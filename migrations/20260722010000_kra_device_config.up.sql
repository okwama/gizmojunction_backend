-- Single-row OSCU device registration for KRA eTIMS. Referenced by
-- internal/taxetims (GetDeviceConfig/SaveDeviceConfig) but never created by
-- any earlier migration — the eTIMS device-init flow would have failed with
-- "relation does not exist" on first use. id is fixed at 1.
CREATE TABLE IF NOT EXISTS public.kra_device_config (
    id integer PRIMARY KEY CHECK (id = 1),
    environment text NOT NULL,
    tin text NOT NULL,
    bhf_id text NOT NULL,
    dvc_srl_no text NOT NULL,
    cmc_key text NOT NULL,
    sdc_id text,
    mrc_no text,
    initialized_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
