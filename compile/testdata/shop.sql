-- Compiled by Precomputing 0.2.0 from shop.precompute.
-- File format 1. Every statement is safe to run again on the same file.

-- Stream lines. Insert events with:
--   INSERT INTO lines (ts, service, level, template, line) VALUES (?, ?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS lines_raw (ts INTEGER NOT NULL, service TEXT NOT NULL, level TEXT NOT NULL, template INTEGER NOT NULL, line TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS lines_raw_ts ON lines_raw (ts);
CREATE VIEW IF NOT EXISTS lines AS SELECT ts, service, level, template, line FROM lines_raw;
CREATE TABLE IF NOT EXISTS lines_win (res INTEGER NOT NULL, w INTEGER NOT NULL, service TEXT NOT NULL, level TEXT NOT NULL, template INTEGER NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, PRIMARY KEY (res, w, service, level, template)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS lines_sample (res INTEGER NOT NULL, w INTEGER NOT NULL, service TEXT NOT NULL, level TEXT NOT NULL, template INTEGER NOT NULL, slot INTEGER NOT NULL, ts INTEGER NOT NULL, line TEXT NOT NULL, PRIMARY KEY (res, w, service, level, template, slot)) WITHOUT ROWID;

-- Stream errors. Insert events with:
--   INSERT INTO errors (ts, service, template, line) VALUES (?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS errors_raw (ts INTEGER NOT NULL, service TEXT NOT NULL, template INTEGER NOT NULL, line TEXT NOT NULL);
CREATE VIEW IF NOT EXISTS errors AS SELECT ts, service, template, line FROM errors_raw;
CREATE TABLE IF NOT EXISTS errors_win (res INTEGER NOT NULL, w INTEGER NOT NULL, service TEXT NOT NULL, template INTEGER NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, PRIMARY KEY (res, w, service, template)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS errors_sample (res INTEGER NOT NULL, w INTEGER NOT NULL, service TEXT NOT NULL, template INTEGER NOT NULL, slot INTEGER NOT NULL, ts INTEGER NOT NULL, line TEXT NOT NULL, PRIMARY KEY (res, w, service, template, slot)) WITHOUT ROWID;

-- Stream web_by_route. Insert events with:
--   INSERT INTO web_by_route (ts, route, line) VALUES (?, ?, ?);
CREATE TABLE IF NOT EXISTS web_by_route_raw (ts INTEGER NOT NULL, route TEXT NOT NULL, line TEXT NOT NULL);
CREATE VIEW IF NOT EXISTS web_by_route AS SELECT ts, route, line FROM web_by_route_raw;
CREATE TABLE IF NOT EXISTS web_by_route_win (res INTEGER NOT NULL, w INTEGER NOT NULL, route TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, PRIMARY KEY (res, w, route)) WITHOUT ROWID;

-- Stream web_ms_by_route. Insert events with:
--   INSERT INTO web_ms_by_route (ts, route, ms, line) VALUES (?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS web_ms_by_route_raw (ts INTEGER NOT NULL, route TEXT NOT NULL, ms REAL NOT NULL, line TEXT NOT NULL);
CREATE VIEW IF NOT EXISTS web_ms_by_route AS SELECT ts, route, ms, line FROM web_ms_by_route_raw;
CREATE TABLE IF NOT EXISTS web_ms_by_route_win (res INTEGER NOT NULL, w INTEGER NOT NULL, route TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, ms_sum REAL NOT NULL, ms_sumsq REAL NOT NULL, ms_min REAL NOT NULL, ms_max REAL NOT NULL, ms_first REAL NOT NULL, ms_last REAL NOT NULL, PRIMARY KEY (res, w, route)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS web_ms_by_route_ms_sk (res INTEGER NOT NULL, w INTEGER NOT NULL, route TEXT NOT NULL, b INTEGER NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (res, w, route, b)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS web_ms_by_route_base (route TEXT NOT NULL, n INTEGER NOT NULL, m REAL NOT NULL, var REAL NOT NULL, PRIMARY KEY (route)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS web_ms_by_route_anomaly (ts INTEGER NOT NULL, route TEXT NOT NULL, ms REAL NOT NULL, line TEXT NOT NULL, z REAL);

-- Stream error_by_service. Insert events with:
--   INSERT INTO error_by_service (ts, service, line) VALUES (?, ?, ?);
CREATE TABLE IF NOT EXISTS error_by_service_raw (ts INTEGER NOT NULL, service TEXT NOT NULL, line TEXT NOT NULL);
CREATE VIEW IF NOT EXISTS error_by_service AS SELECT ts, service, line FROM error_by_service_raw;
CREATE TABLE IF NOT EXISTS error_by_service_win (res INTEGER NOT NULL, w INTEGER NOT NULL, service TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, PRIMARY KEY (res, w, service)) WITHOUT ROWID;

-- Stream payments_by_provider_result. Insert events with:
--   INSERT INTO payments_by_provider_result (ts, provider, result, line) VALUES (?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS payments_by_provider_result_raw (ts INTEGER NOT NULL, provider TEXT NOT NULL, result TEXT NOT NULL, line TEXT NOT NULL);
CREATE VIEW IF NOT EXISTS payments_by_provider_result AS SELECT ts, provider, result, line FROM payments_by_provider_result_raw;
CREATE TABLE IF NOT EXISTS payments_by_provider_result_win (res INTEGER NOT NULL, w INTEGER NOT NULL, provider TEXT NOT NULL, result TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, PRIMARY KEY (res, w, provider, result)) WITHOUT ROWID;

-- Stream checkout_total. Insert events with:
--   INSERT INTO checkout_total (ts, total, line) VALUES (?, ?, ?);
CREATE TABLE IF NOT EXISTS checkout_total_raw (ts INTEGER NOT NULL, total REAL NOT NULL, line TEXT NOT NULL);
CREATE VIEW IF NOT EXISTS checkout_total AS SELECT ts, total, line FROM checkout_total_raw;
CREATE TABLE IF NOT EXISTS checkout_total_win (res INTEGER NOT NULL, w INTEGER NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, total_sum REAL NOT NULL, total_sumsq REAL NOT NULL, total_min REAL NOT NULL, total_max REAL NOT NULL, total_first REAL NOT NULL, total_last REAL NOT NULL, PRIMARY KEY (res, w)) WITHOUT ROWID;

-- Stream search_by_q. Insert events with:
--   INSERT INTO search_by_q (ts, q, line) VALUES (?, ?, ?);
CREATE TABLE IF NOT EXISTS search_by_q_raw (ts INTEGER NOT NULL, q TEXT NOT NULL, line TEXT NOT NULL);
CREATE VIEW IF NOT EXISTS search_by_q AS SELECT ts, q, line FROM search_by_q_raw;
CREATE TABLE IF NOT EXISTS search_by_q_win (res INTEGER NOT NULL, w INTEGER NOT NULL, q TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, PRIMARY KEY (res, w, q)) WITHOUT ROWID;

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
  -- Rollup 10m.
  INSERT INTO lines_win (res, w, service, level, template, n, first_ts, last_ts, an)
    VALUES (600, (CAST(NEW.ts AS INTEGER) / 600 * 600), NEW.service, NEW.level, NEW.template, 1, NEW.ts, NEW.ts, 0)
    ON CONFLICT (res, w, service, level, template) DO UPDATE SET
      n = n + 1,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- A few whole events per 10m window, chosen evenly.
  INSERT INTO lines_sample (res, w, service, level, template, slot, ts, line)
    SELECT 600, (CAST(NEW.ts AS INTEGER) / 600 * 600), NEW.service, NEW.level, NEW.template, CASE WHEN n <= 1 THEN n - 1 ELSE ((n * 1103515245 + 12345) % 2147483648) % 1 END, NEW.ts, NEW.line FROM lines_win
    WHERE res = 600 AND w = (CAST(NEW.ts AS INTEGER) / 600 * 600) AND lines_win.service = NEW.service AND lines_win.level = NEW.level AND lines_win.template = NEW.template AND (n <= 1 OR ((n * 2654435761 + 97) % 4294967296) % n < 1)
    ON CONFLICT (res, w, service, level, template, slot) DO UPDATE SET ts = excluded.ts, line = excluded.line;
END;
CREATE TRIGGER IF NOT EXISTS errors_ingest INSTEAD OF INSERT ON errors
BEGIN
  SELECT RAISE(ABORT, 'errors: ts, service, template, line must not be null') WHERE NEW.ts IS NULL OR NEW.service IS NULL OR NEW.template IS NULL OR NEW.line IS NULL;
  -- Rollup 1m.
  INSERT INTO errors_win (res, w, service, template, n, first_ts, last_ts, an)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.service, NEW.template, 1, NEW.ts, NEW.ts, 0)
    ON CONFLICT (res, w, service, template) DO UPDATE SET
      n = n + 1,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- A few whole events per 1m window, chosen evenly.
  INSERT INTO errors_sample (res, w, service, template, slot, ts, line)
    SELECT 60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.service, NEW.template, CASE WHEN n <= 5 THEN n - 1 ELSE ((n * 1103515245 + 12345) % 2147483648) % 5 END, NEW.ts, NEW.line FROM errors_win
    WHERE res = 60 AND w = (CAST(NEW.ts AS INTEGER) / 60 * 60) AND errors_win.service = NEW.service AND errors_win.template = NEW.template AND (n <= 5 OR ((n * 2654435761 + 97) % 4294967296) % n < 5)
    ON CONFLICT (res, w, service, template, slot) DO UPDATE SET ts = excluded.ts, line = excluded.line;
END;
CREATE TRIGGER IF NOT EXISTS web_by_route_ingest INSTEAD OF INSERT ON web_by_route
BEGIN
  SELECT RAISE(ABORT, 'web_by_route: ts, route, line must not be null') WHERE NEW.ts IS NULL OR NEW.route IS NULL OR NEW.line IS NULL;
  -- Rollup 1m.
  INSERT INTO web_by_route_win (res, w, route, n, first_ts, last_ts, an)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.route, 1, NEW.ts, NEW.ts, 0)
    ON CONFLICT (res, w, route) DO UPDATE SET
      n = n + 1,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
END;
CREATE TRIGGER IF NOT EXISTS web_ms_by_route_ingest INSTEAD OF INSERT ON web_ms_by_route
BEGIN
  SELECT RAISE(ABORT, 'web_ms_by_route: ts, route, ms, line must not be null') WHERE NEW.ts IS NULL OR NEW.route IS NULL OR NEW.ms IS NULL OR NEW.line IS NULL;
  -- An unusual event is kept whole, judged against the baseline before this event updates it.
  INSERT INTO web_ms_by_route_anomaly (ts, route, ms, line, z)
    SELECT NEW.ts, NEW.route, NEW.ms, NEW.line, (ln(NEW.ms) - m) / sqrt(var) FROM web_ms_by_route_base
    WHERE route = NEW.route AND NEW.ms > 0 AND n >= 500 AND (ln(NEW.ms) - m) * (ln(NEW.ms) - m) > 16 * var
      AND coalesce((SELECT an FROM web_ms_by_route_win WHERE res = 60 AND w = (CAST(NEW.ts AS INTEGER) / 60 * 60) AND web_ms_by_route_win.route = NEW.route), 0) < 5;
  -- Rollup 1m.
  INSERT INTO web_ms_by_route_win (res, w, route, n, first_ts, last_ts, an, ms_sum, ms_sumsq, ms_min, ms_max, ms_first, ms_last)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.route, 1, NEW.ts, NEW.ts, coalesce((SELECT CASE WHEN NEW.ms > 0 AND n >= 500 AND (ln(NEW.ms) - m) * (ln(NEW.ms) - m) > 16 * var THEN 1 ELSE 0 END FROM web_ms_by_route_base WHERE route = NEW.route), 0), NEW.ms, NEW.ms * NEW.ms, NEW.ms, NEW.ms, NEW.ms, NEW.ms)
    ON CONFLICT (res, w, route) DO UPDATE SET
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
  INSERT INTO web_ms_by_route_ms_sk (res, w, route, b, n) VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.route, CASE WHEN NEW.ms > 0 THEN CAST(ceil(ln(NEW.ms) / 0.020000666706669435) AS INTEGER) ELSE -1000000 END, 1)
    ON CONFLICT (res, w, route, b) DO UPDATE SET n = n + 1;
  -- The baseline: a plain mean and variance while warming up, then an exponentially weighted one
  -- that each event moves by at most 1.5 standard deviations. Events judged unusual above leave it unchanged.
  INSERT INTO web_ms_by_route_base (route, n, m, var) SELECT NEW.route, 1, ln(NEW.ms), 0.0 WHERE NEW.ms > 0 AND coalesce((SELECT CASE WHEN NEW.ms > 0 AND n >= 500 AND (ln(NEW.ms) - m) * (ln(NEW.ms) - m) > 16 * var THEN 1 ELSE 0 END FROM web_ms_by_route_base WHERE route = NEW.route), 0) = 0
    ON CONFLICT (route) DO UPDATE SET
      n = n + 1,
      m = CASE WHEN n < 500 THEN m + (excluded.m - m) / (n + 1) ELSE m + 0.0003333333333333333 * min(max(excluded.m - m, -1.5 * sqrt(var)), 1.5 * sqrt(var)) END,
      var = CASE WHEN n < 500 THEN (n * var + (excluded.m - m) * (excluded.m - (m + (excluded.m - m) / (n + 1)))) / (n + 1) ELSE (1 - 0.0003333333333333333) * (var + 0.0003333333333333333 * min(max(excluded.m - m, -1.5 * sqrt(var)), 1.5 * sqrt(var)) * min(max(excluded.m - m, -1.5 * sqrt(var)), 1.5 * sqrt(var)) / 0.77846521617447) END;
END;
CREATE TRIGGER IF NOT EXISTS error_by_service_ingest INSTEAD OF INSERT ON error_by_service
BEGIN
  SELECT RAISE(ABORT, 'error_by_service: ts, service, line must not be null') WHERE NEW.ts IS NULL OR NEW.service IS NULL OR NEW.line IS NULL;
  -- Rollup 1m.
  INSERT INTO error_by_service_win (res, w, service, n, first_ts, last_ts, an)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.service, 1, NEW.ts, NEW.ts, 0)
    ON CONFLICT (res, w, service) DO UPDATE SET
      n = n + 1,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
END;
CREATE TRIGGER IF NOT EXISTS payments_by_provider_result_ingest INSTEAD OF INSERT ON payments_by_provider_result
BEGIN
  SELECT RAISE(ABORT, 'payments_by_provider_result: ts, provider, result, line must not be null') WHERE NEW.ts IS NULL OR NEW.provider IS NULL OR NEW.result IS NULL OR NEW.line IS NULL;
  -- Rollup 1m.
  INSERT INTO payments_by_provider_result_win (res, w, provider, result, n, first_ts, last_ts, an)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.provider, NEW.result, 1, NEW.ts, NEW.ts, 0)
    ON CONFLICT (res, w, provider, result) DO UPDATE SET
      n = n + 1,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
END;
CREATE TRIGGER IF NOT EXISTS checkout_total_ingest INSTEAD OF INSERT ON checkout_total
BEGIN
  SELECT RAISE(ABORT, 'checkout_total: ts, total, line must not be null') WHERE NEW.ts IS NULL OR NEW.total IS NULL OR NEW.line IS NULL;
  -- Rollup 1m.
  INSERT INTO checkout_total_win (res, w, n, first_ts, last_ts, an, total_sum, total_sumsq, total_min, total_max, total_first, total_last)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), 1, NEW.ts, NEW.ts, 0, NEW.total, NEW.total * NEW.total, NEW.total, NEW.total, NEW.total, NEW.total)
    ON CONFLICT (res, w) DO UPDATE SET
      n = n + 1,
      total_sum = total_sum + excluded.total_sum,
      total_sumsq = total_sumsq + excluded.total_sumsq,
      total_min = min(total_min, excluded.total_min),
      total_max = max(total_max, excluded.total_max),
      total_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.total_first ELSE total_first END,
      total_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.total_last ELSE total_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
END;
CREATE TRIGGER IF NOT EXISTS search_by_q_ingest INSTEAD OF INSERT ON search_by_q
BEGIN
  SELECT RAISE(ABORT, 'search_by_q: ts, q, line must not be null') WHERE NEW.ts IS NULL OR NEW.q IS NULL OR NEW.line IS NULL;
  -- Rollup 1m.
  INSERT INTO search_by_q_win (res, w, q, n, first_ts, last_ts, an)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.q, 1, NEW.ts, NEW.ts, 0)
    ON CONFLICT (res, w, q) DO UPDATE SET
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
  ('policy', '# Made by precomputing import from the dashboard "Web shop".
# Each stream from logs keeps some of its panels ready; edit freely.

logs {
  format "<timestamp> <level> <service> <message>"
}

# Every line: kept whole on site for 48 hours, counted per template, and one example of
# each template every ten minutes to send upstream.
stream lines from logs {
  key    service text
  key    level text
  key    template integer
  raw    keep 48h
  rollup 1m keep 30d
  rollup 10m keep 30d
  samples 1 per 10m
}

# Errors: whole lines, up to five a minute of each kind, to send upstream.
stream errors from logs where level = "ERROR" {
  key    service text
  key    template integer
  rollup 1m keep 30d
  samples 5 per 1m
}

# "Requests a minute by route": count
stream web_by_route from logs where service = "web" {
  key    route text
  rollup 1m keep 30d
}

# "p95 latency by route": p95 of ms
stream web_ms_by_route from logs where service = "web" {
  key    route text
  value  ms real
  rollup 1m keep 30d quantiles ms
  anomalies ms log z > 4 keep 5 per 1m
}

# "Errors a minute by service": count
stream error_by_service from logs where level = "ERROR" {
  key    service text
  rollup 1m keep 30d
}

# "Payments by provider and result": count
stream payments_by_provider_result from logs where service = "payments" {
  key    provider text
  key    result text
  rollup 1m keep 30d
}

# "Revenue a minute": sum of total
stream checkout_total from logs where service = "checkout" {
  value  total real
  rollup 1m keep 30d
}

# "Top searches": top 10 by count
stream search_by_q from logs where service = "search" {
  key    q text
  rollup 1m keep 30d
}
'),
  ('distill', '-- Distill for shop.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream lines.
DELETE FROM lines_raw WHERE ts < :now - 172800;
DELETE FROM lines_win WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM lines_win WHERE res = 600 AND w <= :now - 2592600;
DELETE FROM lines_sample WHERE res = 600 AND w <= :now - 2592600;

-- Stream errors.
DELETE FROM errors_win WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM errors_sample WHERE res = 60 AND w <= :now - 2592060;

-- Stream web_by_route.
DELETE FROM web_by_route_win WHERE res = 60 AND w <= :now - 2592060;

-- Stream web_ms_by_route.
DELETE FROM web_ms_by_route_win WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM web_ms_by_route_ms_sk WHERE res = 60 AND w <= :now - 2592060;

-- Stream error_by_service.
DELETE FROM error_by_service_win WHERE res = 60 AND w <= :now - 2592060;

-- Stream payments_by_provider_result.
DELETE FROM payments_by_provider_result_win WHERE res = 60 AND w <= :now - 2592060;

-- Stream checkout_total.
DELETE FROM checkout_total_win WHERE res = 60 AND w <= :now - 2592060;

-- Stream search_by_q.
DELETE FROM search_by_q_win WHERE res = 60 AND w <= :now - 2592060;
');
CREATE TABLE IF NOT EXISTS _precomputing_objects (name TEXT PRIMARY KEY, kind TEXT NOT NULL, stream TEXT NOT NULL, detail TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing_objects (name, kind, stream, detail) VALUES
  ('lines', 'stream', 'lines', '{"derived":[],"from":"logs","insert":"INSERT INTO lines (ts, service, level, template, line) VALUES (...)","keys":["service","level","template"],"raw_keep_seconds":172800,"raw_table":"lines_raw","values":[],"where":{}}'),
  ('lines_win', 'windows', 'lines', '{"numbers":[],"rollups":[{"keep_seconds":2592000,"quantiles":[],"res_seconds":60},{"keep_seconds":2592000,"quantiles":[],"res_seconds":600}]}'),
  ('lines_sample', 'samples', 'lines', '{"n":1,"per_seconds":600}'),
  ('errors', 'stream', 'errors', '{"derived":[],"from":"logs","insert":"INSERT INTO errors (ts, service, template, line) VALUES (...)","keys":["service","template"],"raw_keep_seconds":0,"raw_table":"errors_raw","values":[],"where":{"level":"ERROR"}}'),
  ('errors_win', 'windows', 'errors', '{"numbers":[],"rollups":[{"keep_seconds":2592000,"quantiles":[],"res_seconds":60}]}'),
  ('errors_sample', 'samples', 'errors', '{"n":5,"per_seconds":60}'),
  ('web_by_route', 'stream', 'web_by_route', '{"derived":[],"from":"logs","insert":"INSERT INTO web_by_route (ts, route, line) VALUES (...)","keys":["route"],"raw_keep_seconds":0,"raw_table":"web_by_route_raw","values":[],"where":{"service":"web"}}'),
  ('web_by_route_win', 'windows', 'web_by_route', '{"numbers":[],"rollups":[{"keep_seconds":2592000,"quantiles":[],"res_seconds":60}]}'),
  ('web_ms_by_route', 'stream', 'web_ms_by_route', '{"derived":[],"from":"logs","insert":"INSERT INTO web_ms_by_route (ts, route, ms, line) VALUES (...)","keys":["route"],"raw_keep_seconds":0,"raw_table":"web_ms_by_route_raw","values":["ms"],"where":{"service":"web"}}'),
  ('web_ms_by_route_win', 'windows', 'web_ms_by_route', '{"numbers":["ms"],"rollups":[{"keep_seconds":2592000,"quantiles":["ms"],"res_seconds":60}]}'),
  ('web_ms_by_route_ms_sk', 'sketch', 'web_ms_by_route', '{"accuracy":0.01,"estimate":"2 * pow(1.02020202020202, b) / (1.02020202020202 + 1)","gamma":1.02020202020202,"res_seconds":[60],"value":"ms","zero_bucket":-1000000}'),
  ('web_ms_by_route_anomaly', 'anomalies', 'web_ms_by_route', '{"baseline_table":"web_ms_by_route_base","keep":5,"log":true,"memory":3000,"per_seconds":60,"value":"ms","warmup":500,"z":4}'),
  ('error_by_service', 'stream', 'error_by_service', '{"derived":[],"from":"logs","insert":"INSERT INTO error_by_service (ts, service, line) VALUES (...)","keys":["service"],"raw_keep_seconds":0,"raw_table":"error_by_service_raw","values":[],"where":{"level":"ERROR"}}'),
  ('error_by_service_win', 'windows', 'error_by_service', '{"numbers":[],"rollups":[{"keep_seconds":2592000,"quantiles":[],"res_seconds":60}]}'),
  ('payments_by_provider_result', 'stream', 'payments_by_provider_result', '{"derived":[],"from":"logs","insert":"INSERT INTO payments_by_provider_result (ts, provider, result, line) VALUES (...)","keys":["provider","result"],"raw_keep_seconds":0,"raw_table":"payments_by_provider_result_raw","values":[],"where":{"service":"payments"}}'),
  ('payments_by_provider_result_win', 'windows', 'payments_by_provider_result', '{"numbers":[],"rollups":[{"keep_seconds":2592000,"quantiles":[],"res_seconds":60}]}'),
  ('checkout_total', 'stream', 'checkout_total', '{"derived":[],"from":"logs","insert":"INSERT INTO checkout_total (ts, total, line) VALUES (...)","keys":[],"raw_keep_seconds":0,"raw_table":"checkout_total_raw","values":["total"],"where":{"service":"checkout"}}'),
  ('checkout_total_win', 'windows', 'checkout_total', '{"numbers":["total"],"rollups":[{"keep_seconds":2592000,"quantiles":[],"res_seconds":60}]}'),
  ('search_by_q', 'stream', 'search_by_q', '{"derived":[],"from":"logs","insert":"INSERT INTO search_by_q (ts, q, line) VALUES (...)","keys":["q"],"raw_keep_seconds":0,"raw_table":"search_by_q_raw","values":[],"where":{"service":"search"}}'),
  ('search_by_q_win', 'windows', 'search_by_q', '{"numbers":[],"rollups":[{"keep_seconds":2592000,"quantiles":[],"res_seconds":60}]}'),
  ('logs', 'logs', '', '{"depth":4,"fields":["timestamp","level","service","message"],"format":"<timestamp> <level> <service> <message>","masks":[],"similarity":0.5,"templates_table":"_precomputing_templates"}');
