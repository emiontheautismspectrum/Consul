-- +goose Up
-- Anonymous Chamber of Commerce statistics (problem 21). The report reads only these
-- precomputed snapshots, never ops, so no request filter can single out a person.
-- Groups smaller than 5 entrepreneurs are never written (k-anonymity, k = 5).
CREATE TABLE palata_snapshots (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    period_end  DATE        NOT NULL,
    window_days SMALLINT    NOT NULL CHECK (window_days IN (30, 90)),
    cut         VARCHAR(10) NOT NULL CHECK (cut IN ('all', 'sector', 'market', 'trend')),
    cut_key     VARCHAR(20) NOT NULL,
    n           INT         NOT NULL CHECK (n >= 5),
    metrics     JSONB       NOT NULL,
    synthetic   BOOLEAN     NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (period_end, window_days, cut, cut_key)
);
CREATE INDEX idx_palata_window_period ON palata_snapshots (window_days, period_end DESC);

-- Synthetic demo entrepreneurs (cmd seed): excluded from nothing, but always flagged.
ALTER TABLE users ADD COLUMN synthetic BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE users DROP COLUMN synthetic;
DROP TABLE palata_snapshots;
