CREATE TABLE IF NOT EXISTS users (
	id text PRIMARY KEY,
	identifier text UNIQUE NOT NULL,
	password_hash text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS users_identifier ON users(identifier);

CREATE TABLE IF NOT EXISTS user_saved_listings (
	user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	platform text NOT NULL,
	listing_id text NOT NULL,
	saved_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (user_id, platform, listing_id),
	FOREIGN KEY (platform, listing_id) REFERENCES listings(platform, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_saved_listings_user ON user_saved_listings(user_id, saved_at DESC);
