-- Compiled by Precomputing 0.2.0 from latency.precompute.
-- File format 1. Every statement is safe to run again on the same file.

-- Stream latency. Insert events with:
--   INSERT INTO latency (ts, endpoint, ms) VALUES (?, ?, ?);
CREATE TABLE IF NOT EXISTS latency_raw (ts INTEGER NOT NULL, endpoint TEXT NOT NULL, ms REAL NOT NULL);
CREATE INDEX IF NOT EXISTS latency_raw_ts ON latency_raw (ts);
CREATE VIEW IF NOT EXISTS latency AS SELECT ts, endpoint, ms FROM latency_raw;
CREATE TABLE IF NOT EXISTS latency_win (res INTEGER NOT NULL, w INTEGER NOT NULL, endpoint TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, ms_sum REAL NOT NULL, ms_sumsq REAL NOT NULL, ms_min REAL NOT NULL, ms_max REAL NOT NULL, ms_first REAL NOT NULL, ms_last REAL NOT NULL, PRIMARY KEY (res, w, endpoint)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS latency_ms_sk (res INTEGER NOT NULL, w INTEGER NOT NULL, endpoint TEXT NOT NULL, b INTEGER NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (res, w, endpoint, b)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS latency_sample (res INTEGER NOT NULL, w INTEGER NOT NULL, endpoint TEXT NOT NULL, slot INTEGER NOT NULL, ts INTEGER NOT NULL, ms REAL NOT NULL, PRIMARY KEY (res, w, endpoint, slot)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS latency_base (endpoint TEXT NOT NULL, n INTEGER NOT NULL, m REAL NOT NULL, var REAL NOT NULL, PRIMARY KEY (endpoint)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS latency_anomaly (ts INTEGER NOT NULL, endpoint TEXT NOT NULL, ms REAL NOT NULL, z REAL);

-- Precomputed answers from stream latency by endpoint.
CREATE TABLE IF NOT EXISTS _pc_latency_by_endpoint (endpoint TEXT NOT NULL, n INTEGER NOT NULL, ms_sum REAL NOT NULL, PRIMARY KEY (endpoint)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS requests AS SELECT endpoint, n AS value FROM _pc_latency_by_endpoint;
CREATE VIEW IF NOT EXISTS avg_ms AS SELECT endpoint, ms_sum / n AS value FROM _pc_latency_by_endpoint;

-- Precomputed quantiles of latency.ms by endpoint.
CREATE TABLE IF NOT EXISTS _pcq_latency_ms_by_endpoint (endpoint TEXT NOT NULL, b INTEGER NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (endpoint, b)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS p99_ms AS
  WITH c AS (SELECT endpoint, b, sum(n) OVER (PARTITION BY endpoint ORDER BY b ROWS UNBOUNDED PRECEDING) AS cum, sum(n) OVER (PARTITION BY endpoint) AS tot FROM _pcq_latency_ms_by_endpoint)
  SELECT endpoint, CASE WHEN min(b) = -1000000 THEN 0.0 ELSE 2 * pow(1.02020202020202, min(b)) / (1.02020202020202 + 1) END AS value FROM c WHERE cum >= 0.99 * tot GROUP BY endpoint;

-- The work done on every insert.
CREATE TRIGGER IF NOT EXISTS latency_ingest INSTEAD OF INSERT ON latency
BEGIN
  SELECT RAISE(ABORT, 'latency: ts, endpoint, ms must not be null') WHERE NEW.ts IS NULL OR NEW.endpoint IS NULL OR NEW.ms IS NULL;
  INSERT INTO latency_raw (ts, endpoint, ms) VALUES (NEW.ts, NEW.endpoint, NEW.ms);
  -- An unusual event is kept whole, judged against the baseline before this event updates it.
  INSERT INTO latency_anomaly (ts, endpoint, ms, z)
    SELECT NEW.ts, NEW.endpoint, NEW.ms, (ln(NEW.ms) - m) / sqrt(var) FROM latency_base
    WHERE endpoint = NEW.endpoint AND NEW.ms > 0 AND n >= 500 AND (ln(NEW.ms) - m) * (ln(NEW.ms) - m) > 16 * var
      AND coalesce((SELECT an FROM latency_win WHERE res = 60 AND w = (CAST(NEW.ts AS INTEGER) / 60 * 60) AND latency_win.endpoint = NEW.endpoint), 0) < 20;
  -- Rollup 10s.
  INSERT INTO latency_win (res, w, endpoint, n, first_ts, last_ts, an, ms_sum, ms_sumsq, ms_min, ms_max, ms_first, ms_last)
    VALUES (10, (CAST(NEW.ts AS INTEGER) / 10 * 10), NEW.endpoint, 1, NEW.ts, NEW.ts, coalesce((SELECT CASE WHEN NEW.ms > 0 AND n >= 500 AND (ln(NEW.ms) - m) * (ln(NEW.ms) - m) > 16 * var THEN 1 ELSE 0 END FROM latency_base WHERE endpoint = NEW.endpoint), 0), NEW.ms, NEW.ms * NEW.ms, NEW.ms, NEW.ms, NEW.ms, NEW.ms)
    ON CONFLICT (res, w, endpoint) DO UPDATE SET
      n = n + 1,
      ms_sum = ms_sum + excluded.ms_sum,
      ms_sumsq = ms_sumsq + excluded.ms_sumsq,
      ms_min = min(ms_min, excluded.ms_min),
      ms_max = max(ms_max, excluded.ms_max),
      ms_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.ms_first ELSE ms_first END,
      ms_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.ms_last ELSE ms_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Rollup 1m.
  INSERT INTO latency_win (res, w, endpoint, n, first_ts, last_ts, an, ms_sum, ms_sumsq, ms_min, ms_max, ms_first, ms_last)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.endpoint, 1, NEW.ts, NEW.ts, coalesce((SELECT CASE WHEN NEW.ms > 0 AND n >= 500 AND (ln(NEW.ms) - m) * (ln(NEW.ms) - m) > 16 * var THEN 1 ELSE 0 END FROM latency_base WHERE endpoint = NEW.endpoint), 0), NEW.ms, NEW.ms * NEW.ms, NEW.ms, NEW.ms, NEW.ms, NEW.ms)
    ON CONFLICT (res, w, endpoint) DO UPDATE SET
      n = n + 1,
      ms_sum = ms_sum + excluded.ms_sum,
      ms_sumsq = ms_sumsq + excluded.ms_sumsq,
      ms_min = min(ms_min, excluded.ms_min),
      ms_max = max(ms_max, excluded.ms_max),
      ms_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.ms_first ELSE ms_first END,
      ms_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.ms_last ELSE ms_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  INSERT INTO latency_ms_sk (res, w, endpoint, b, n) VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.endpoint, CASE WHEN NEW.ms > 0 THEN CAST(ceil(ln(NEW.ms) / 0.020000666706669435) AS INTEGER) ELSE -1000000 END, 1)
    ON CONFLICT (res, w, endpoint, b) DO UPDATE SET n = n + 1;
  -- Rollup 1h.
  INSERT INTO latency_win (res, w, endpoint, n, first_ts, last_ts, an, ms_sum, ms_sumsq, ms_min, ms_max, ms_first, ms_last)
    VALUES (3600, (CAST(NEW.ts AS INTEGER) / 3600 * 3600), NEW.endpoint, 1, NEW.ts, NEW.ts, coalesce((SELECT CASE WHEN NEW.ms > 0 AND n >= 500 AND (ln(NEW.ms) - m) * (ln(NEW.ms) - m) > 16 * var THEN 1 ELSE 0 END FROM latency_base WHERE endpoint = NEW.endpoint), 0), NEW.ms, NEW.ms * NEW.ms, NEW.ms, NEW.ms, NEW.ms, NEW.ms)
    ON CONFLICT (res, w, endpoint) DO UPDATE SET
      n = n + 1,
      ms_sum = ms_sum + excluded.ms_sum,
      ms_sumsq = ms_sumsq + excluded.ms_sumsq,
      ms_min = min(ms_min, excluded.ms_min),
      ms_max = max(ms_max, excluded.ms_max),
      ms_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.ms_first ELSE ms_first END,
      ms_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.ms_last ELSE ms_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  INSERT INTO latency_ms_sk (res, w, endpoint, b, n) VALUES (3600, (CAST(NEW.ts AS INTEGER) / 3600 * 3600), NEW.endpoint, CASE WHEN NEW.ms > 0 THEN CAST(ceil(ln(NEW.ms) / 0.020000666706669435) AS INTEGER) ELSE -1000000 END, 1)
    ON CONFLICT (res, w, endpoint, b) DO UPDATE SET n = n + 1;
  -- A few whole events per 1m window, chosen evenly.
  INSERT INTO latency_sample (res, w, endpoint, slot, ts, ms)
    SELECT 60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.endpoint, CASE WHEN n <= 3 THEN n - 1 ELSE ((n * 1103515245 + 12345) % 2147483648) % 3 END, NEW.ts, NEW.ms FROM latency_win
    WHERE res = 60 AND w = (CAST(NEW.ts AS INTEGER) / 60 * 60) AND latency_win.endpoint = NEW.endpoint AND (n <= 3 OR ((n * 2654435761 + 97) % 4294967296) % n < 3)
    ON CONFLICT (res, w, endpoint, slot) DO UPDATE SET ts = excluded.ts, ms = excluded.ms;
  -- Precomputed answers by endpoint.
  INSERT INTO _pc_latency_by_endpoint (endpoint, n, ms_sum) VALUES (NEW.endpoint, 1, NEW.ms)
    ON CONFLICT (endpoint) DO UPDATE SET n = n + 1, ms_sum = ms_sum + excluded.ms_sum;
  -- Precomputed quantiles of ms by endpoint.
  INSERT INTO _pcq_latency_ms_by_endpoint (endpoint, b, n) VALUES (NEW.endpoint, CASE WHEN NEW.ms > 0 THEN CAST(ceil(ln(NEW.ms) / 0.020000666706669435) AS INTEGER) ELSE -1000000 END, 1)
    ON CONFLICT (endpoint, b) DO UPDATE SET n = n + 1;
  -- The baseline: a plain mean and variance while warming up, then an exponentially weighted one
  -- that each event moves by at most 1.5 standard deviations. Events judged unusual above leave it unchanged.
  INSERT INTO latency_base (endpoint, n, m, var) SELECT NEW.endpoint, 1, ln(NEW.ms), 0.0 WHERE NEW.ms > 0 AND coalesce((SELECT CASE WHEN NEW.ms > 0 AND n >= 500 AND (ln(NEW.ms) - m) * (ln(NEW.ms) - m) > 16 * var THEN 1 ELSE 0 END FROM latency_base WHERE endpoint = NEW.endpoint), 0) = 0
    ON CONFLICT (endpoint) DO UPDATE SET
      n = n + 1,
      m = CASE WHEN n < 500 THEN m + (excluded.m - m) / (n + 1) ELSE m + 0.0003333333333333333 * min(max(excluded.m - m, -1.5 * sqrt(var)), 1.5 * sqrt(var)) END,
      var = CASE WHEN n < 500 THEN (n * var + (excluded.m - m) * (excluded.m - (m + (excluded.m - m) / (n + 1)))) / (n + 1) ELSE (1 - 0.0003333333333333333) * (var + 0.0003333333333333333 * min(max(excluded.m - m, -1.5 * sqrt(var)), 1.5 * sqrt(var)) * min(max(excluded.m - m, -1.5 * sqrt(var)), 1.5 * sqrt(var)) / 0.77846521617447) END;
END;

-- What this file holds, for any tool that opens it.
CREATE TABLE IF NOT EXISTS _precomputing (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing (key, value) VALUES
  ('format', '1'),
  ('compiler', 'precomputing 0.2.0'),
  ('policy', '# API latency for a small web service: the policy behind Demo 1.
# Every request is one event: when it happened, which endpoint, how long it took.

stream latency {
  key   endpoint text
  value ms real

  raw     keep 5m                        # recent requests stay whole for five minutes
  rollup  10s keep 24h                   # then 10-second summaries for a day
  rollup  1m  keep 30d  quantiles ms     # 1-minute summaries with p99 for a month
  rollup  1h  keep 1y   quantiles ms     # hourly summaries for a year

  samples 3 per 1m                       # three whole requests kept from every minute
  anomalies ms log z > 4 keep 20 per 1m  # unusually slow or fast requests kept whole
}

precompute requests = count(latency) by endpoint
precompute avg_ms   = avg(latency.ms) by endpoint
precompute p99_ms   = p99(latency.ms) by endpoint
'),
  ('distill', '-- Distill for latency.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream latency.
DELETE FROM latency_raw WHERE ts < :now - 300;
DELETE FROM latency_win WHERE res = 10 AND w <= :now - 86410;
DELETE FROM latency_win WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM latency_ms_sk WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM latency_sample WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM latency_win WHERE res = 3600 AND w <= :now - 31539600;
DELETE FROM latency_ms_sk WHERE res = 3600 AND w <= :now - 31539600;
');
CREATE TABLE IF NOT EXISTS _precomputing_objects (name TEXT PRIMARY KEY, kind TEXT NOT NULL, stream TEXT NOT NULL, detail TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing_objects (name, kind, stream, detail) VALUES
  ('latency', 'stream', 'latency', '{"derived":[],"insert":"INSERT INTO latency (ts, endpoint, ms) VALUES (...)","keys":["endpoint"],"raw_keep_seconds":300,"raw_table":"latency_raw","values":["ms"]}'),
  ('latency_win', 'windows', 'latency', '{"numbers":["ms"],"rollups":[{"keep_seconds":86400,"quantiles":[],"res_seconds":10},{"keep_seconds":2592000,"quantiles":["ms"],"res_seconds":60},{"keep_seconds":31536000,"quantiles":["ms"],"res_seconds":3600}]}'),
  ('latency_ms_sk', 'sketch', 'latency', '{"accuracy":0.01,"estimate":"2 * pow(1.02020202020202, b) / (1.02020202020202 + 1)","gamma":1.02020202020202,"res_seconds":[60,3600],"value":"ms","zero_bucket":-1000000}'),
  ('latency_sample', 'samples', 'latency', '{"n":3,"per_seconds":60}'),
  ('latency_anomaly', 'anomalies', 'latency', '{"baseline_table":"latency_base","keep":20,"log":true,"memory":3000,"per_seconds":60,"value":"ms","warmup":500,"z":4}'),
  ('requests', 'precompute', 'latency', '{"by":["endpoint"],"function":"count","per":null,"state":"_pc_latency_by_endpoint","value":null}'),
  ('avg_ms', 'precompute', 'latency', '{"by":["endpoint"],"function":"avg","per":null,"state":"_pc_latency_by_endpoint","value":"ms"}'),
  ('p99_ms', 'precompute', 'latency', '{"accuracy":0.01,"by":["endpoint"],"function":"p99","gamma":1.02020202020202,"per":null,"quantile":0.99,"state":"_pcq_latency_ms_by_endpoint","value":"ms"}');
