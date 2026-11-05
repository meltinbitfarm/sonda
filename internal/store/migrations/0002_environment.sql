-- 0002: events.environment (D23). The SDK no longer drops test builds: it marks
-- every event production, testflight or development. Rows stored before this
-- migration came from SDK 0.1.0, which sent only production builds, so the
-- default is correct for them and for any client that omits the field.
--
-- Additive: one column with a constant default (SQLite only rewrites the
-- schema, plus one pass to check the CHECK on existing rows). The three lookup
-- indexes are rebuilt with environment right after app_id, and one on
-- (app_id, environment, ts) is added for range-only reads (totals, newest
-- events). The analytics queries always constrain environment (= or IN over
-- the three values), so the rest of each index stays usable.

ALTER TABLE events ADD COLUMN environment TEXT NOT NULL DEFAULT 'production'
    CHECK (environment IN ('production', 'testflight', 'development'));

DROP INDEX events_app_name_ts;
DROP INDEX events_app_user_ts;
DROP INDEX events_app_session_ts;

CREATE INDEX events_app_env_name_ts    ON events (app_id, environment, name, ts);
CREATE INDEX events_app_env_user_ts    ON events (app_id, environment, user_id, ts);
CREATE INDEX events_app_env_session_ts ON events (app_id, environment, session_id, ts);
CREATE INDEX events_app_env_ts         ON events (app_id, environment, ts);
