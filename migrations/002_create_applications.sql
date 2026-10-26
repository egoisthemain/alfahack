CREATE TABLE IF NOT EXISTS applications(
    application_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    company_id bigint NOT NULL REFERENCES companies(company_id),
    product text NOT NULL CHECK (product  in ('EXPRESS', 'EQUIPMENT')),
    amount bigint NOT NULL CHECK (amount > 0),
    term_months integer NOT NULL CHECK (term_months > 0),
    purpose text NOT NULL,
    declared_annual_revenue bigint NOT NULL CHECK (declared_annual_revenue > 0),
    bki_consent boolean NOT NULL,
    equipment_cost bigint CHECK (equipment_cost IS NULL OR equipment_cost > 0),
    supplier_document_id text,
    status text NOT NULL DEFAULT 'DRAFT',
    created_at timestamptz NOT NULL DEFAULT now()
);

