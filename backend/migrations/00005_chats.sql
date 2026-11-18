-- +goose Up
-- Consul chat history. tokens/latency per message give the real AI cost per user.
CREATE TABLE chats (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title      VARCHAR(200) NOT NULL DEFAULT '',
    topic      VARCHAR(30)  NOT NULL DEFAULT 'general',
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_chats_user_updated ON chats (user_id, updated_at DESC);

CREATE TABLE messages (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_id    UUID         NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    user_id    UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       VARCHAR(10)  NOT NULL CHECK (role IN ('user','assistant')),
    text       TEXT         NOT NULL,
    reply      JSONB,
    tool_calls JSONB,
    model      VARCHAR(64),
    tokens_in  INT          NOT NULL DEFAULT 0,
    tokens_out INT          NOT NULL DEFAULT 0,
    latency_ms INT          NOT NULL DEFAULT 0,
    fallback   VARCHAR(30),  -- why a template answered instead of the LLM; NULL = LLM answer
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_messages_chat_created ON messages (chat_id, created_at);
CREATE INDEX idx_messages_user_created ON messages (user_id, created_at);

-- +goose Down
DROP TABLE messages;
DROP TABLE chats;
