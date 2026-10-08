-- Compiled by Precomputing 0.2.0 from dejavu.precompute.
-- File format 1. Every statement is safe to run again on the same file.

-- Stream lines. Insert events with:
--   INSERT INTO lines (ts, service, level, template, line) VALUES (?, ?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS lines_raw (ts INTEGER NOT NULL, service TEXT NOT NULL, level TEXT NOT NULL, template INTEGER NOT NULL, line TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS lines_raw_ts ON lines_raw (ts);
CREATE VIEW IF NOT EXISTS lines AS SELECT ts, service, level, template, line FROM lines_raw;
CREATE TABLE IF NOT EXISTS lines_win (res INTEGER NOT NULL, w INTEGER NOT NULL, service TEXT NOT NULL, level TEXT NOT NULL, template INTEGER NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, PRIMARY KEY (res, w, service, level, template)) WITHOUT ROWID;

-- Log templates. The Engine learns them from each line's message, separately for each service
-- and level, and numbers them; a stream's template key is that number. initial holds the tokens
-- a template started from.
CREATE TABLE IF NOT EXISTS _precomputing_templates (id INTEGER PRIMARY KEY, service TEXT NOT NULL, level TEXT NOT NULL, template TEXT NOT NULL, initial TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, example TEXT NOT NULL);

-- The work done on every insert.
CREATE TRIGGER IF NOT EXISTS lines_ingest INSTEAD OF INSERT ON lines
BEGIN
  SELECT RAISE(ABORT, 'lines: ts, service, level, template, line must not be null') WHERE NEW.ts IS NULL OR NEW.service IS NULL OR NEW.level IS NULL OR NEW.template IS NULL OR NEW.line IS NULL;
  INSERT INTO lines_raw (ts, service, level, template, line) VALUES (NEW.ts, NEW.service, NEW.level, NEW.template, NEW.line);
  -- Rollup 1m.
  INSERT INTO lines_win (res, w, service, level, template, n, first_ts, last_ts, an)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.service, NEW.level, NEW.template, 1, NEW.ts, NEW.ts, 0)
    ON CONFLICT (res, w, service, level, template) DO UPDATE SET
      n = n + 1,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Rollup 1h.
  INSERT INTO lines_win (res, w, service, level, template, n, first_ts, last_ts, an)
    VALUES (3600, (CAST(NEW.ts AS INTEGER) / 3600 * 3600), NEW.service, NEW.level, NEW.template, 1, NEW.ts, NEW.ts, 0)
    ON CONFLICT (res, w, service, level, template) DO UPDATE SET
      n = n + 1,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
END;

-- What this file holds, for any tool that opens it.
CREATE TABLE IF NOT EXISTS _precomputing (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing (key, value) VALUES
  ('format', '1'),
  ('compiler', 'precomputing 0.2.0'),
  ('policy', '# Incident deja vu: the policy behind Demo 6.
# Every log line is counted per template, a minute at a time for a week and an hour at a time
# for 90 days, and kept whole for ten minutes. The comparison with the incidents written down
# is plain SQL beside the policy''s tables (demo/dejavu/app/dejavu.sql).

logs {
  format "<timestamp> <level> <service> <message>"
}

stream lines from logs {
  key    service text
  key    level text
  key    template integer
  raw    keep 10m
  rollup 1m keep 7d
  rollup 1h keep 90d
}
'),
  ('distill', '-- Distill for dejavu.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream lines.
DELETE FROM lines_raw WHERE ts < :now - 600;
DELETE FROM lines_win WHERE res = 60 AND w <= :now - 604860;
DELETE FROM lines_win WHERE res = 3600 AND w <= :now - 7779600;
');
CREATE TABLE IF NOT EXISTS _precomputing_objects (name TEXT PRIMARY KEY, kind TEXT NOT NULL, stream TEXT NOT NULL, detail TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing_objects (name, kind, stream, detail) VALUES
  ('lines', 'stream', 'lines', '{"derived":[],"from":"logs","insert":"INSERT INTO lines (ts, service, level, template, line) VALUES (...)","keys":["service","level","template"],"raw_keep_seconds":600,"raw_table":"lines_raw","values":[],"where":{}}'),
  ('lines_win', 'windows', 'lines', '{"numbers":[],"rollups":[{"keep_seconds":604800,"quantiles":[],"res_seconds":60},{"keep_seconds":7776000,"quantiles":[],"res_seconds":3600}]}'),
  ('logs', 'logs', '', '{"depth":4,"fields":["timestamp","level","service","message"],"format":"<timestamp> <level> <service> <message>","masks":[],"similarity":0.5,"templates_table":"_precomputing_templates"}');
