-- +goose Up
-- Market prices (problem 19). Sources: narx.stat.uz weekly averages (official statistics),
-- anonymous medians from consenting users' purchases (k >= 5 users), and flagged sample rows.
CREATE TABLE prices (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product     VARCHAR(80)  NOT NULL,   -- normalized key, e.g. orikqoqi
    name        VARCHAR(120) NOT NULL,   -- display name as written by the source
    unit        VARCHAR(20)  NOT NULL DEFAULT 'kg',
    market      VARCHAR(40)  NOT NULL,   -- republic | toshkent_city | chorsu | oloy | ...
    price       BIGINT       NOT NULL CHECK (price > 0),   -- so'm per unit
    n           INT          NOT NULL DEFAULT 0,           -- users behind a user_op median (>= 5), 0 otherwise
    observed_at DATE         NOT NULL,
    source      VARCHAR(12)  NOT NULL CHECK (source IN ('narx_stat', 'user_op', 'seed')),
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (product, market, source, observed_at),
    CHECK (source <> 'user_op' OR n >= 5)
);
CREATE INDEX idx_prices_product_observed ON prices (product, observed_at DESC);

-- +goose Down
DROP TABLE prices;
