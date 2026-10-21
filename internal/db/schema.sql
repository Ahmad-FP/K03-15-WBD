CREATE TABLE IF NOT EXISTS users (
	id bigserial PRIMARY KEY,
	username text NOT NULL UNIQUE,
	password_hash text NOT NULL,
	is_admin boolean NOT NULL DEFAULT false
);

INSERT INTO users (username, password_hash, is_admin)
VALUES ('admin', '$2a$10$Gu2t1XRUhEUk1RfjT0YQeOcc.Ci9R1On0p735.8PPFG49LRQDmwb2', true)
ON CONFLICT (username) DO NOTHING;

CREATE TABLE IF NOT EXISTS sessions (
	id text PRIMARY KEY,
	prev_id text,
	prev_until timestamptz,
	user_id bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL,
	last_seen_at timestamptz NOT NULL,
	rotated_at timestamptz NOT NULL
);
