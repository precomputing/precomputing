-- Compiled by Precomputing 0.1.1 from wikipedia.precompute.
-- File format 1. Every statement is safe to run again on the same file.

-- Stream edits. Insert events with:
--   INSERT INTO edits (ts, wiki, kind, who, bytes) VALUES (?, ?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS edits_raw (ts INTEGER NOT NULL, wiki TEXT NOT NULL, kind TEXT NOT NULL, who TEXT NOT NULL, bytes INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS edits_raw_ts ON edits_raw (ts);
CREATE VIEW IF NOT EXISTS edits AS SELECT ts, wiki, kind, who, bytes FROM edits_raw;
CREATE TABLE IF NOT EXISTS edits_win (res INTEGER NOT NULL, w INTEGER NOT NULL, wiki TEXT NOT NULL, kind TEXT NOT NULL, who TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, bytes_sum REAL NOT NULL, bytes_sumsq REAL NOT NULL, bytes_min REAL NOT NULL, bytes_max REAL NOT NULL, bytes_first REAL NOT NULL, bytes_last REAL NOT NULL, PRIMARY KEY (res, w, wiki, kind, who)) WITHOUT ROWID;

-- Stream article_edits. Insert events with:
--   INSERT INTO article_edits (ts, wiki, title, bytes) VALUES (?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS article_edits_raw (ts INTEGER NOT NULL, wiki TEXT NOT NULL, title TEXT NOT NULL, bytes INTEGER NOT NULL);
CREATE VIEW IF NOT EXISTS article_edits AS SELECT ts, wiki, title, bytes FROM article_edits_raw;
CREATE TABLE IF NOT EXISTS article_edits_win (res INTEGER NOT NULL, w INTEGER NOT NULL, wiki TEXT NOT NULL, title TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, bytes_sum REAL NOT NULL, bytes_sumsq REAL NOT NULL, bytes_min REAL NOT NULL, bytes_max REAL NOT NULL, bytes_first REAL NOT NULL, bytes_last REAL NOT NULL, PRIMARY KEY (res, w, wiki, title)) WITHOUT ROWID;

-- Stream article_editors. Insert events with:
--   INSERT INTO article_editors (ts, wiki, title, editor, bytes) VALUES (?, ?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS article_editors_raw (ts INTEGER NOT NULL, wiki TEXT NOT NULL, title TEXT NOT NULL, editor INTEGER NOT NULL, bytes INTEGER NOT NULL);
CREATE VIEW IF NOT EXISTS article_editors AS SELECT ts, wiki, title, editor, bytes FROM article_editors_raw;
CREATE TABLE IF NOT EXISTS article_editors_win (res INTEGER NOT NULL, w INTEGER NOT NULL, wiki TEXT NOT NULL, title TEXT NOT NULL, editor INTEGER NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, bytes_sum REAL NOT NULL, bytes_sumsq REAL NOT NULL, bytes_min REAL NOT NULL, bytes_max REAL NOT NULL, bytes_first REAL NOT NULL, bytes_last REAL NOT NULL, PRIMARY KEY (res, w, wiki, title, editor)) WITHOUT ROWID;

-- Precomputed answers from stream edits.
CREATE TABLE IF NOT EXISTS _pc_edits (g INTEGER PRIMARY KEY CHECK (g = 1), n INTEGER NOT NULL);
CREATE VIEW IF NOT EXISTS changes AS SELECT n AS value FROM _pc_edits;

-- Precomputed answers from stream edits by wiki per day.
CREATE TABLE IF NOT EXISTS _pc_edits_by_wiki_per_day (wiki TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, bytes_sum REAL NOT NULL, PRIMARY KEY (wiki, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS changes_day AS SELECT wiki, period, n AS value FROM _pc_edits_by_wiki_per_day;
CREATE VIEW IF NOT EXISTS growth_day AS SELECT wiki, period, bytes_sum AS value FROM _pc_edits_by_wiki_per_day;

-- The work done on every insert.
CREATE TRIGGER IF NOT EXISTS edits_ingest INSTEAD OF INSERT ON edits
BEGIN
  SELECT RAISE(ABORT, 'edits: ts, wiki, kind, who, bytes must not be null') WHERE NEW.ts IS NULL OR NEW.wiki IS NULL OR NEW.kind IS NULL OR NEW.who IS NULL OR NEW.bytes IS NULL;
  INSERT INTO edits_raw (ts, wiki, kind, who, bytes) VALUES (NEW.ts, NEW.wiki, NEW.kind, NEW.who, NEW.bytes);
  -- Rollup 10s.
  INSERT INTO edits_win (res, w, wiki, kind, who, n, first_ts, last_ts, an, bytes_sum, bytes_sumsq, bytes_min, bytes_max, bytes_first, bytes_last)
    VALUES (10, (CAST(NEW.ts AS INTEGER) / 10 * 10), NEW.wiki, NEW.kind, NEW.who, 1, NEW.ts, NEW.ts, 0, NEW.bytes, NEW.bytes * NEW.bytes, NEW.bytes, NEW.bytes, NEW.bytes, NEW.bytes)
    ON CONFLICT (res, w, wiki, kind, who) DO UPDATE SET
      n = n + 1,
      bytes_sum = bytes_sum + excluded.bytes_sum,
      bytes_sumsq = bytes_sumsq + excluded.bytes_sumsq,
      bytes_min = min(bytes_min, excluded.bytes_min),
      bytes_max = max(bytes_max, excluded.bytes_max),
      bytes_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.bytes_first ELSE bytes_first END,
      bytes_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.bytes_last ELSE bytes_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Rollup 1m.
  INSERT INTO edits_win (res, w, wiki, kind, who, n, first_ts, last_ts, an, bytes_sum, bytes_sumsq, bytes_min, bytes_max, bytes_first, bytes_last)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.wiki, NEW.kind, NEW.who, 1, NEW.ts, NEW.ts, 0, NEW.bytes, NEW.bytes * NEW.bytes, NEW.bytes, NEW.bytes, NEW.bytes, NEW.bytes)
    ON CONFLICT (res, w, wiki, kind, who) DO UPDATE SET
      n = n + 1,
      bytes_sum = bytes_sum + excluded.bytes_sum,
      bytes_sumsq = bytes_sumsq + excluded.bytes_sumsq,
      bytes_min = min(bytes_min, excluded.bytes_min),
      bytes_max = max(bytes_max, excluded.bytes_max),
      bytes_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.bytes_first ELSE bytes_first END,
      bytes_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.bytes_last ELSE bytes_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Rollup 1h.
  INSERT INTO edits_win (res, w, wiki, kind, who, n, first_ts, last_ts, an, bytes_sum, bytes_sumsq, bytes_min, bytes_max, bytes_first, bytes_last)
    VALUES (3600, (CAST(NEW.ts AS INTEGER) / 3600 * 3600), NEW.wiki, NEW.kind, NEW.who, 1, NEW.ts, NEW.ts, 0, NEW.bytes, NEW.bytes * NEW.bytes, NEW.bytes, NEW.bytes, NEW.bytes, NEW.bytes)
    ON CONFLICT (res, w, wiki, kind, who) DO UPDATE SET
      n = n + 1,
      bytes_sum = bytes_sum + excluded.bytes_sum,
      bytes_sumsq = bytes_sumsq + excluded.bytes_sumsq,
      bytes_min = min(bytes_min, excluded.bytes_min),
      bytes_max = max(bytes_max, excluded.bytes_max),
      bytes_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.bytes_first ELSE bytes_first END,
      bytes_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.bytes_last ELSE bytes_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Precomputed answers.
  INSERT INTO _pc_edits (g, n) VALUES (1, 1)
    ON CONFLICT (g) DO UPDATE SET n = n + 1;
  -- Precomputed answers by wiki per day.
  INSERT INTO _pc_edits_by_wiki_per_day (wiki, period, n, bytes_sum) VALUES (NEW.wiki, strftime('%Y-%m-%d', NEW.ts, 'unixepoch'), 1, NEW.bytes)
    ON CONFLICT (wiki, period) DO UPDATE SET n = n + 1, bytes_sum = bytes_sum + excluded.bytes_sum;
END;
CREATE TRIGGER IF NOT EXISTS article_edits_ingest INSTEAD OF INSERT ON article_edits
BEGIN
  SELECT RAISE(ABORT, 'article_edits: ts, wiki, title, bytes must not be null') WHERE NEW.ts IS NULL OR NEW.wiki IS NULL OR NEW.title IS NULL OR NEW.bytes IS NULL;
  -- Rollup 1m.
  INSERT INTO article_edits_win (res, w, wiki, title, n, first_ts, last_ts, an, bytes_sum, bytes_sumsq, bytes_min, bytes_max, bytes_first, bytes_last)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.wiki, NEW.title, 1, NEW.ts, NEW.ts, 0, NEW.bytes, NEW.bytes * NEW.bytes, NEW.bytes, NEW.bytes, NEW.bytes, NEW.bytes)
    ON CONFLICT (res, w, wiki, title) DO UPDATE SET
      n = n + 1,
      bytes_sum = bytes_sum + excluded.bytes_sum,
      bytes_sumsq = bytes_sumsq + excluded.bytes_sumsq,
      bytes_min = min(bytes_min, excluded.bytes_min),
      bytes_max = max(bytes_max, excluded.bytes_max),
      bytes_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.bytes_first ELSE bytes_first END,
      bytes_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.bytes_last ELSE bytes_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
END;
CREATE TRIGGER IF NOT EXISTS article_editors_ingest INSTEAD OF INSERT ON article_editors
BEGIN
  SELECT RAISE(ABORT, 'article_editors: ts, wiki, title, editor, bytes must not be null') WHERE NEW.ts IS NULL OR NEW.wiki IS NULL OR NEW.title IS NULL OR NEW.editor IS NULL OR NEW.bytes IS NULL;
  -- Rollup 1m.
  INSERT INTO article_editors_win (res, w, wiki, title, editor, n, first_ts, last_ts, an, bytes_sum, bytes_sumsq, bytes_min, bytes_max, bytes_first, bytes_last)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.wiki, NEW.title, NEW.editor, 1, NEW.ts, NEW.ts, 0, NEW.bytes, NEW.bytes * NEW.bytes, NEW.bytes, NEW.bytes, NEW.bytes, NEW.bytes)
    ON CONFLICT (res, w, wiki, title, editor) DO UPDATE SET
      n = n + 1,
      bytes_sum = bytes_sum + excluded.bytes_sum,
      bytes_sumsq = bytes_sumsq + excluded.bytes_sumsq,
      bytes_min = min(bytes_min, excluded.bytes_min),
      bytes_max = max(bytes_max, excluded.bytes_max),
      bytes_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.bytes_first ELSE bytes_first END,
      bytes_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.bytes_last ELSE bytes_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
END;

-- What this file holds, for any tool that opens it.
CREATE TABLE IF NOT EXISTS _precomputing (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing (key, value) VALUES
  ('format', '1'),
  ('compiler', 'precomputing 0.1.1'),
  ('policy', '# Wikipedia''s live edits: the policy behind Demo 5.
# Every change to every Wikimedia wiki, as the public stream of recent changes sends it. Each
# change goes into edits. A change to an article also goes into article_edits, and one made by
# a person rather than a bot into article_editors, with the editor as a number made from the
# user name, so the file never holds a name.

stream edits {
  key    wiki text               # the wiki''s address, such as en.wikipedia.org
  key    kind text               # edit, new, log, categorize or external
  key    who text                # bot or person
  value  bytes integer           # how much longer the page got; 0 for changes that are not edits
  raw    keep 10m
  rollup 10s keep 1h
  rollup 1m  keep 7d
  rollup 1h  keep 1y
}

stream article_edits {
  key    wiki text
  key    title text
  value  bytes integer
  rollup 1m keep 1h
}

stream article_editors {
  key    wiki text
  key    title text
  key    editor integer
  value  bytes integer
  rollup 1m keep 1h
}

precompute changes     = count(edits)
precompute changes_day = count(edits) by wiki per day
precompute growth_day  = sum(edits.bytes) by wiki per day
'),
  ('distill', '-- Distill for wikipedia.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream edits.
DELETE FROM edits_raw WHERE ts < :now - 600;
DELETE FROM edits_win WHERE res = 10 AND w <= :now - 3610;
DELETE FROM edits_win WHERE res = 60 AND w <= :now - 604860;
DELETE FROM edits_win WHERE res = 3600 AND w <= :now - 31539600;

-- Stream article_edits.
DELETE FROM article_edits_win WHERE res = 60 AND w <= :now - 3660;

-- Stream article_editors.
DELETE FROM article_editors_win WHERE res = 60 AND w <= :now - 3660;
');
CREATE TABLE IF NOT EXISTS _precomputing_objects (name TEXT PRIMARY KEY, kind TEXT NOT NULL, stream TEXT NOT NULL, detail TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing_objects (name, kind, stream, detail) VALUES
  ('edits', 'stream', 'edits', '{"derived":[],"insert":"INSERT INTO edits (ts, wiki, kind, who, bytes) VALUES (...)","keys":["wiki","kind","who"],"raw_keep_seconds":600,"raw_table":"edits_raw","values":["bytes"]}'),
  ('edits_win', 'windows', 'edits', '{"numbers":["bytes"],"rollups":[{"keep_seconds":3600,"quantiles":[],"res_seconds":10},{"keep_seconds":604800,"quantiles":[],"res_seconds":60},{"keep_seconds":31536000,"quantiles":[],"res_seconds":3600}]}'),
  ('article_edits', 'stream', 'article_edits', '{"derived":[],"insert":"INSERT INTO article_edits (ts, wiki, title, bytes) VALUES (...)","keys":["wiki","title"],"raw_keep_seconds":0,"raw_table":"article_edits_raw","values":["bytes"]}'),
  ('article_edits_win', 'windows', 'article_edits', '{"numbers":["bytes"],"rollups":[{"keep_seconds":3600,"quantiles":[],"res_seconds":60}]}'),
  ('article_editors', 'stream', 'article_editors', '{"derived":[],"insert":"INSERT INTO article_editors (ts, wiki, title, editor, bytes) VALUES (...)","keys":["wiki","title","editor"],"raw_keep_seconds":0,"raw_table":"article_editors_raw","values":["bytes"]}'),
  ('article_editors_win', 'windows', 'article_editors', '{"numbers":["bytes"],"rollups":[{"keep_seconds":3600,"quantiles":[],"res_seconds":60}]}'),
  ('changes', 'precompute', 'edits', '{"by":[],"function":"count","per":null,"state":"_pc_edits","value":null}'),
  ('changes_day', 'precompute', 'edits', '{"by":["wiki"],"function":"count","per":"day","state":"_pc_edits_by_wiki_per_day","value":null}'),
  ('growth_day', 'precompute', 'edits', '{"by":["wiki"],"function":"sum","per":"day","state":"_pc_edits_by_wiki_per_day","value":"bytes"}');
