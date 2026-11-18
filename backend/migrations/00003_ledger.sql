-- +goose Up
-- Bookkeeping. The ops journal is the single source of truth: balances, debts, profit and
-- reports are computed from it and never stored separately. Money is BIGINT so'm.

CREATE TABLE businesses (
    user_id    UUID PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    trade      VARCHAR(200) NOT NULL DEFAULT '',
    sector     VARCHAR(20)  NOT NULL DEFAULT 'other'    CHECK (sector IN ('dried_fruit','food','clothes','services','other')),
    market     VARCHAR(20)  NOT NULL DEFAULT 'other'    CHECK (market IN ('chorsu','oloy','parkent','quyliq','other')),
    status     VARCHAR(10)  NOT NULL DEFAULT 'none'     CHECK (status IN ('yatt','oob','mchj','none')),
    regime     VARCHAR(10)  NOT NULL DEFAULT 'turnover' CHECK (regime IN ('turnover','vat6','vat12')),
    reserve    BIGINT       NOT NULL DEFAULT 0          CHECK (reserve >= 0),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE business_status_history (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    old_status VARCHAR(10),
    new_status VARCHAR(10) NOT NULL,
    changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_bsh_changed ON business_status_history (changed_at);

-- Customers who owe (nasiya) and suppliers. name_key = lowercased Latin transliteration, so
-- "Бекзод" and "bekzod" are the same person.
CREATE TABLE counterparties (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind       VARCHAR(10)  NOT NULL CHECK (kind IN ('customer','supplier')),
    name       VARCHAR(100) NOT NULL,
    name_key   VARCHAR(100) NOT NULL,
    phone      VARCHAR(20),
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (user_id, kind, name_key)
);

CREATE TABLE ops (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind            VARCHAR(10)  NOT NULL CHECK (kind IN ('sale','buy','exp','sup','nas','repaid','fix','opening')),
    amount          BIGINT       NOT NULL CHECK (kind = 'fix' OR amount > 0),
    method          VARCHAR(10)  NOT NULL CHECK (method IN ('naqd','karta','none')),
    label           VARCHAR(200) NOT NULL DEFAULT '',
    counterparty_id UUID REFERENCES counterparties (id) ON DELETE SET NULL,
    fiscal          BOOLEAN      NOT NULL DEFAULT false,
    source          VARCHAR(10)  NOT NULL DEFAULT 'manual' CHECK (source IN ('text','voice','photo','manual','seed')),
    raw_text        VARCHAR(500),
    items           JSONB,
    occurred_at     TIMESTAMPTZ  NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    idempotency_key VARCHAR(100),
    response        JSONB,       -- first response, replayed for a repeated Idempotency-Key
    UNIQUE (user_id, idempotency_key)
);
CREATE INDEX idx_ops_user_occurred ON ops (user_id, occurred_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_ops_counterparty ON ops (counterparty_id) WHERE deleted_at IS NULL;

CREATE TABLE obligations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind            VARCHAR(10)  NOT NULL CHECK (kind IN ('rent','supplier','loan','other')),
    label           VARCHAR(200) NOT NULL DEFAULT '',
    amount          BIGINT       NOT NULL CHECK (amount > 0),
    due_day         SMALLINT     CHECK (due_day BETWEEN 1 AND 31),
    due_date        DATE,
    recurring       BOOLEAN      NOT NULL DEFAULT false,
    counterparty_id UUID REFERENCES counterparties (id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CHECK (due_day IS NOT NULL OR due_date IS NOT NULL)
);
CREATE INDEX idx_obligations_user ON obligations (user_id);

-- "Consul memory": facts the user can see, edit and erase.
CREATE TABLE memory_facts (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key            VARCHAR(100) NOT NULL,
    value          TEXT         NOT NULL,
    source_chat_id UUID,
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (user_id, key)
);

-- "Why don't you register?" survey for the Chamber report.
CREATE TABLE survey_answers (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    question   VARCHAR(50) NOT NULL,
    answer     VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE survey_answers;
DROP TABLE memory_facts;
DROP TABLE obligations;
DROP TABLE ops;
DROP TABLE counterparties;
DROP TABLE business_status_history;
DROP TABLE businesses;
