CREATE TABLE IF NOT EXISTS tiles (
    id BIGSERIAL PRIMARY KEY,
    public_id CHAR(26) UNIQUE NOT NULL,
    file_path TEXT NOT NULL,
    url TEXT NOT NULL,
    generated_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tiles_expires_at
ON tiles (expires_at);
