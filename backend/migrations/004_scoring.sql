ALTER TABLE applications ADD COLUMN IF NOT EXISTS submitted_at timestamptz;
ALTER TABLE applications ADD COLUMN IF NOT EXISTS decision jsonb;
ALTER TABLE applications ADD COLUMN IF NOT EXISTS source_snapshot jsonb;
ALTER TABLE applications ADD COLUMN IF NOT EXISTS scoring_error text;
ALTER TABLE application_events ADD COLUMN IF NOT EXISTS comment text;
ALTER TABLE application_events ADD COLUMN IF NOT EXISTS override boolean NOT NULL DEFAULT false;
CREATE TABLE IF NOT EXISTS source_profiles (
    company_id bigint NOT NULL REFERENCES companies(company_id),
    kind text NOT NULL CHECK (kind IN ('business_checks','financial_profile','bki_profile','registry')),
    snapshot jsonb NOT NULL,
    PRIMARY KEY (company_id, kind)
);
CREATE TABLE IF NOT EXISTS scoring_runs (
    run_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    application_id bigint NOT NULL REFERENCES applications(application_id),
    sources jsonb NOT NULL,
    model_response jsonb,
    decision jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS application_events_application_idx ON application_events(application_id,event_id);
CREATE TABLE IF NOT EXISTS demo_documents (
    document_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    company_id bigint NOT NULL REFERENCES companies(company_id),
    name text NOT NULL,
    content bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
