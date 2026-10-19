CREATE TABLE IF NOT EXISTS events (
  id TEXT PRIMARY KEY,
  payload TEXT,
  created_at INTEGER NOT NULL,
  acknowledged_at INTEGER
);
CREATE INDEX IF NOT EXISTS pending_events ON events(created_at, id) WHERE acknowledged_at IS NULL;
CREATE INDEX IF NOT EXISTS acknowledged_events ON events(acknowledged_at) WHERE acknowledged_at IS NOT NULL;
