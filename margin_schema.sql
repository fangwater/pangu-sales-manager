CREATE TABLE IF NOT EXISTS profit_accounting_records (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,kind text NOT NULL,record_key text NOT NULL,
 payload jsonb NOT NULL,version integer NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),deleted_at timestamptz,UNIQUE(kind,record_key)
);
CREATE TABLE IF NOT EXISTS profit_accounting_history (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,record_id bigint NOT NULL,action text NOT NULL,
 before_payload jsonb,after_payload jsonb,created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS profit_margin_reports (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,params jsonb NOT NULL,payload text NOT NULL CHECK(jsonb_typeof(payload::jsonb)='object'),
 sha256 text NOT NULL UNIQUE,created_at timestamptz NOT NULL DEFAULT now()
);
