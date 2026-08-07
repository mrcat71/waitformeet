-- Initial schema.
--
-- Instants are stored as INTEGER unix seconds (UTC). SQLite has no date type, and
-- integers compare and sort correctly in SQL without any parsing or timezone
-- ambiguity, which is what the "next future event" and "newest notes" queries need.
-- Use datetime(col, 'unixepoch') when inspecting the database by hand.
--
-- Tables are declared before anything that references them, so the file also reads
-- as a dependency order.

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- A user may hold a password, an OIDC identity, or both. An empty password_hash
-- simply means password login is not available for that person.
CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    email         TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    is_admin      INTEGER NOT NULL DEFAULT 0,
    password_hash TEXT    NOT NULL DEFAULT '',
    oidc_subject  TEXT    NOT NULL DEFAULT '',
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER,
    created_by    INTEGER          REFERENCES users(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX users_email_idx ON users (email);
CREATE UNIQUE INDEX users_oidc_subject_idx ON users (oidc_subject) WHERE oidc_subject <> '';

CREATE TABLE invites (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   TEXT    NOT NULL UNIQUE,
    email        TEXT    NOT NULL,
    display_name TEXT    NOT NULL DEFAULT '',
    is_admin     INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    used_at      INTEGER,
    created_by   INTEGER          REFERENCES users(id) ON DELETE SET NULL
);

-- id holds the SHA-256 of the cookie token, never the token itself, so a database
-- leak does not hand over live sessions.
CREATE TABLE sessions (
    id         TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    user_agent TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE media (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    filename       TEXT    NOT NULL,
    thumb_filename TEXT    NOT NULL,
    caption        TEXT    NOT NULL DEFAULT '',
    width          INTEGER NOT NULL DEFAULT 0,
    height         INTEGER NOT NULL DEFAULT 0,
    uploaded_by    INTEGER          REFERENCES users(id) ON DELETE SET NULL,
    created_at     INTEGER NOT NULL,
    sort_order     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX media_sort_idx ON media (sort_order, id);

CREATE TABLE settings (
    id                    INTEGER PRIMARY KEY CHECK (id = 1),
    site_title            TEXT    NOT NULL DEFAULT '',
    tagline               TEXT    NOT NULL DEFAULT '',
    partner_a_name        TEXT    NOT NULL DEFAULT '',
    partner_a_city        TEXT    NOT NULL DEFAULT '',
    partner_a_timezone    TEXT    NOT NULL DEFAULT 'UTC',
    partner_b_name        TEXT    NOT NULL DEFAULT '',
    partner_b_city        TEXT    NOT NULL DEFAULT '',
    partner_b_timezone    TEXT    NOT NULL DEFAULT 'UTC',
    accent_color          TEXT    NOT NULL DEFAULT '#e5687f',
    default_locale        TEXT    NOT NULL DEFAULT 'en',
    background_media_id   INTEGER          REFERENCES media(id) ON DELETE SET NULL,
    separated_at          INTEGER,
    -- Visibility per section: 'public', 'logged-in' or 'admin'. Only admins may change these.
    vis_countdown         TEXT    NOT NULL DEFAULT 'public'    CHECK (vis_countdown  IN ('public','logged-in','admin')),
    vis_clocks            TEXT    NOT NULL DEFAULT 'public'    CHECK (vis_clocks     IN ('public','logged-in','admin')),
    vis_milestones        TEXT    NOT NULL DEFAULT 'public'    CHECK (vis_milestones IN ('public','logged-in','admin')),
    vis_notes             TEXT    NOT NULL DEFAULT 'logged-in' CHECK (vis_notes      IN ('public','logged-in','admin')),
    vis_gallery           TEXT    NOT NULL DEFAULT 'logged-in' CHECK (vis_gallery    IN ('public','logged-in','admin')),
    quotes_enabled        INTEGER NOT NULL DEFAULT 0,
    weather_enabled       INTEGER NOT NULL DEFAULT 0,
    auto_advance          INTEGER NOT NULL DEFAULT 1,
    updated_at            INTEGER NOT NULL DEFAULT 0
);

-- The main countdown is simply the row with kind='main'; the partial unique index
-- below makes "exactly one main event" a database invariant rather than a convention.
CREATE TABLE events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT    NOT NULL CHECK (kind IN ('main','milestone')),
    title       TEXT    NOT NULL,
    emoji       TEXT    NOT NULL DEFAULT '',
    target_at   INTEGER NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    visible     INTEGER NOT NULL DEFAULT 1,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX events_single_main_idx ON events (kind) WHERE kind = 'main';
CREATE INDEX events_target_idx ON events (target_at);

-- author_name is denormalised on purpose: removing a person's account should not
-- blank out the notes they left.
CREATE TABLE notes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    author_id   INTEGER          REFERENCES users(id) ON DELETE SET NULL,
    author_name TEXT    NOT NULL DEFAULT '',
    body        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    pinned      INTEGER NOT NULL DEFAULT 0,
    visible     INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX notes_created_idx ON notes (created_at DESC);

-- An empty locale means the quote is shown for every language.
CREATE TABLE quotes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    text       TEXT    NOT NULL,
    locale     TEXT    NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);

INSERT INTO settings (id, updated_at) VALUES (1, 0);
