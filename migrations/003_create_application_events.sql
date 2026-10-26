CREATE TABLE IF NOT EXISTS application_events (
    event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    application_id bigint NOT NULL REFERENCES applications(application_id),
    from_status text NOT NULL,
    to_status text NOT NULL,
    actor text NOT NULL,
    reason_code text,
    created_at timestamptz NOT NULL DEFAULT now()
);