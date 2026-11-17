-- +goose Up
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           VARCHAR(255) NOT NULL,
    password_hash   VARCHAR(255) NOT NULL,
    salt            VARCHAR(64)  NOT NULL,
    lang            VARCHAR(2)   NOT NULL DEFAULT 'ru',
    consent_at      TIMESTAMPTZ,
    consent_version VARCHAR(16),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_users_email ON users (email);

CREATE TABLE business_plans (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title       VARCHAR(255) NOT NULL,
    input       JSONB        NOT NULL,
    content     TEXT         NOT NULL,
    structured  JSONB,
    tool_calls  JSONB        NOT NULL DEFAULT '[]',
    lang        VARCHAR(2)   NOT NULL,
    model       VARCHAR(64)  NOT NULL,
    tokens_in   INT          NOT NULL DEFAULT 0,
    tokens_out  INT          NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_bplans_user_created ON business_plans (user_id, created_at DESC);

-- History of all calculator runs. kind = loan | tax | breakeven.
-- amount/rate/term are filled for loans (as in the spec) to allow cheap aggregate queries.
CREATE TABLE loan_calculations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID          NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind         VARCHAR(16)   NOT NULL DEFAULT 'loan',
    amount       NUMERIC(18,2),
    rate         NUMERIC(7,3),
    term         INT,
    input        JSONB         NOT NULL,
    result_jsonb JSONB         NOT NULL,
    created_at   TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX idx_lcalc_user_created ON loan_calculations (user_id, created_at DESC);
CREATE INDEX idx_lcalc_user_kind_created ON loan_calculations (user_id, kind, created_at DESC);

-- +goose Down
DROP TABLE loan_calculations;
DROP TABLE business_plans;
DROP TABLE users;
