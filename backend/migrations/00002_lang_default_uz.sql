-- +goose Up
-- Uzbek is the default language of the product.
ALTER TABLE users ALTER COLUMN lang SET DEFAULT 'uz';

-- +goose Down
ALTER TABLE users ALTER COLUMN lang SET DEFAULT 'ru';
