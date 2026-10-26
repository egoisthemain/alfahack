CREATE TABLE IF NOT EXISTS companies (
    company_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    inn varchar(12) NOT NULL UNIQUE CHECK (inn ~ '^([0-9]{10}|[0-9]{12})$'),
    name text NOT NULL,
    legal_form text NOT NULL CHECK (legal_form in ('IP', 'OOO')),
    registration_date date NOT NULL,
    status text NOT NULL CHECK (status in ('ACTIVE', 'LIQUIDATING', 'BANKRUPT')),
    okved text NOT NULL
);