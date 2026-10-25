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

CREATE TABLE IF NOT EXISTS platform_groups (
    id bigserial PRIMARY KEY,
    name text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS group_members (
    group_id bigint NOT NULL REFERENCES platform_groups(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_group_members_user_id ON group_members(user_id);

CREATE TABLE IF NOT EXISTS managed_databases (
    id bigserial PRIMARY KEY,
    name text NOT NULL UNIQUE,
    host_label text NOT NULL DEFAULT '',
    encrypted_dsn bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS group_database_access (
    group_id bigint NOT NULL REFERENCES platform_groups(id) ON DELETE CASCADE,
    database_id bigint NOT NULL REFERENCES managed_databases(id) ON DELETE CASCADE,
    access_level text NOT NULL CHECK (access_level IN ('view_only', 'view_edit')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, database_id)
);
CREATE INDEX IF NOT EXISTS idx_group_database_access_database_id ON group_database_access(database_id);

CREATE TABLE IF NOT EXISTS migrations (
    id bigserial PRIMARY KEY,
    database_id bigint NOT NULL REFERENCES managed_databases(id) ON DELETE CASCADE,
    version integer NOT NULL,
    name text NOT NULL,
    sql text NOT NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'success', 'failed')),
    error text NOT NULL DEFAULT '',
    created_by bigint REFERENCES users(id) ON DELETE SET NULL,
    executed_by bigint REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    executed_at timestamptz,
    UNIQUE (database_id, version)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_migrations_one_pending ON migrations(database_id) WHERE status <> 'success';
