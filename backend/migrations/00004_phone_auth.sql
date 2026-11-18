-- +goose Up
-- Phone OTP login: email and password become optional. Second (voluntary) consent for
-- anonymous Chamber statistics, roles and UI preferences.
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE users ALTER COLUMN salt DROP NOT NULL;
ALTER TABLE users ADD COLUMN phone VARCHAR(16);
ALTER TABLE users ADD COLUMN role VARCHAR(10) NOT NULL DEFAULT 'user' CHECK (role IN ('user','chamber','admin'));
ALTER TABLE users ADD COLUMN stats_consent_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN stats_consent_version VARCHAR(16);
ALTER TABLE users ADD COLUMN prefs JSONB NOT NULL DEFAULT '{}';
CREATE UNIQUE INDEX idx_users_phone ON users (phone) WHERE phone IS NOT NULL;
ALTER TABLE users ADD CONSTRAINT users_login_present CHECK (email IS NOT NULL OR phone IS NOT NULL);

-- +goose Down
ALTER TABLE users DROP CONSTRAINT users_login_present;
DROP INDEX idx_users_phone;
ALTER TABLE users DROP COLUMN prefs;
ALTER TABLE users DROP COLUMN stats_consent_version;
ALTER TABLE users DROP COLUMN stats_consent_at;
ALTER TABLE users DROP COLUMN role;
ALTER TABLE users DROP COLUMN phone;
