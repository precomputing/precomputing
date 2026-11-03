-- Compiled by Precomputing 0.3.0 from otel.precompute.
-- File format 1. Every statement is safe to run again on the same file.

-- Stream errors. Insert events with:
--   INSERT INTO errors (ts, service, template, line) VALUES (?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS errors_raw (ts INTEGER NOT NULL, service TEXT NOT NULL, template INTEGER NOT NULL, line TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS errors_raw_ts ON errors_raw (ts);
CREATE VIEW IF NOT EXISTS errors AS SELECT ts, service, template, line FROM errors_raw;
CREATE TABLE IF NOT EXISTS errors_win (res INTEGER NOT NULL, w INTEGER NOT NULL, service TEXT NOT NULL, template INTEGER NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, PRIMARY KEY (res, w, service, template)) WITHOUT ROWID;

-- Stream requests. Insert events with:
--   INSERT INTO requests (ts, service, http_route, http_response_status_code, count, sum) VALUES (?, ?, ?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS requests_raw (ts INTEGER NOT NULL, service TEXT NOT NULL, http_route TEXT NOT NULL, http_response_status_code INTEGER NOT NULL, count INTEGER NOT NULL, sum REAL NOT NULL);
CREATE VIEW IF NOT EXISTS requests AS SELECT ts, service, http_route, http_response_status_code, count, sum FROM requests_raw;
CREATE TABLE IF NOT EXISTS requests_win (res INTEGER NOT NULL, w INTEGER NOT NULL, service TEXT NOT NULL, http_route TEXT NOT NULL, http_response_status_code INTEGER NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, count_sum REAL NOT NULL, count_sumsq REAL NOT NULL, count_min REAL NOT NULL, count_max REAL NOT NULL, count_first REAL NOT NULL, count_last REAL NOT NULL, sum_sum REAL NOT NULL, sum_sumsq REAL NOT NULL, sum_min REAL NOT NULL, sum_max REAL NOT NULL, sum_first REAL NOT NULL, sum_last REAL NOT NULL, PRIMARY KEY (res, w, service, http_route, http_response_status_code)) WITHOUT ROWID;

-- Stream queue. Insert events with:
--   INSERT INTO queue (ts, service, depth) VALUES (?, ?, ?);
CREATE TABLE IF NOT EXISTS queue_raw (ts INTEGER NOT NULL, service TEXT NOT NULL, depth REAL NOT NULL);
CREATE VIEW IF NOT EXISTS queue AS SELECT ts, service, depth FROM queue_raw;
CREATE TABLE IF NOT EXISTS queue_win (res INTEGER NOT NULL, w INTEGER NOT NULL, service TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, depth_sum REAL NOT NULL, depth_sumsq REAL NOT NULL, depth_min REAL NOT NULL, depth_max REAL NOT NULL, depth_first REAL NOT NULL, depth_last REAL NOT NULL, PRIMARY KEY (res, w, service)) WITHOUT ROWID;

-- Precomputed answers from stream errors by service per hour.
CREATE TABLE IF NOT EXISTS _pc_errors_by_service_per_hour (service TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (service, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS errors_hour AS SELECT service, period, n AS value FROM _pc_errors_by_service_per_hour;

-- Precomputed answers from stream requests by service, http_route per day.
CREATE TABLE IF NOT EXISTS _pc_requests_by_service_http_route_per_day (service TEXT NOT NULL, http_route TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, count_sum REAL NOT NULL, sum_sum REAL NOT NULL, PRIMARY KEY (service, http_route, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS requests_day AS SELECT service, http_route, period, count_sum AS value FROM _pc_requests_by_service_http_route_per_day;
CREATE VIEW IF NOT EXISTS seconds_day AS SELECT service, http_route, period, sum_sum AS value FROM _pc_requests_by_service_http_route_per_day;

-- Precomputed answers from stream queue by service per day.
CREATE TABLE IF NOT EXISTS _pc_queue_by_service_per_day (service TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, depth_max REAL NOT NULL, PRIMARY KEY (service, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS queue_max_day AS SELECT service, period, depth_max AS value FROM _pc_queue_by_service_per_day;

-- Log templates. The Engine learns them from each line's message, separately for each service
-- and level, and numbers them; a stream's template key is that number. initial holds the tokens
-- a template started from.
CREATE TABLE IF NOT EXISTS _precomputing_templates (id INTEGER PRIMARY KEY, service TEXT NOT NULL, level TEXT NOT NULL, template TEXT NOT NULL, initial TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, example TEXT NOT NULL);

-- The work done on every insert.
CREATE TRIGGER IF NOT EXISTS errors_ingest INSTEAD OF INSERT ON errors
BEGIN
  SELECT RAISE(ABORT, 'errors: ts, service, template, line must not be null') WHERE NEW.ts IS NULL OR NEW.service IS NULL OR NEW.template IS NULL OR NEW.line IS NULL;
  INSERT INTO errors_raw (ts, service, template, line) VALUES (NEW.ts, NEW.service, NEW.template, NEW.line);
  -- Rollup 1m.
  INSERT INTO errors_win (res, w, service, template, n, first_ts, last_ts, an)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.service, NEW.template, 1, NEW.ts, NEW.ts, 0)
    ON CONFLICT (res, w, service, template) DO UPDATE SET
      n = n + 1,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Precomputed answers by service per hour.
  INSERT INTO _pc_errors_by_service_per_hour (service, period, n) VALUES (NEW.service, strftime('%Y-%m-%dT%H', NEW.ts, 'unixepoch'), 1)
    ON CONFLICT (service, period) DO UPDATE SET n = n + 1;
END;
CREATE TRIGGER IF NOT EXISTS requests_ingest INSTEAD OF INSERT ON requests
BEGIN
  SELECT RAISE(ABORT, 'requests: ts, service, http_route, http_response_status_code, count, sum must not be null') WHERE NEW.ts IS NULL OR NEW.service IS NULL OR NEW.http_route IS NULL OR NEW.http_response_status_code IS NULL OR NEW.count IS NULL OR NEW.sum IS NULL;
  -- Rollup 1m.
  INSERT INTO requests_win (res, w, service, http_route, http_response_status_code, n, first_ts, last_ts, an, count_sum, count_sumsq, count_min, count_max, count_first, count_last, sum_sum, sum_sumsq, sum_min, sum_max, sum_first, sum_last)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.service, NEW.http_route, NEW.http_response_status_code, 1, NEW.ts, NEW.ts, 0, NEW.count, NEW.count * NEW.count, NEW.count, NEW.count, NEW.count, NEW.count, NEW.sum, NEW.sum * NEW.sum, NEW.sum, NEW.sum, NEW.sum, NEW.sum)
    ON CONFLICT (res, w, service, http_route, http_response_status_code) DO UPDATE SET
      n = n + 1,
      count_sum = count_sum + excluded.count_sum,
      count_sumsq = count_sumsq + excluded.count_sumsq,
      count_min = min(count_min, excluded.count_min),
      count_max = max(count_max, excluded.count_max),
      count_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.count_first ELSE count_first END,
      count_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.count_last ELSE count_last END,
      sum_sum = sum_sum + excluded.sum_sum,
      sum_sumsq = sum_sumsq + excluded.sum_sumsq,
      sum_min = min(sum_min, excluded.sum_min),
      sum_max = max(sum_max, excluded.sum_max),
      sum_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.sum_first ELSE sum_first END,
      sum_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.sum_last ELSE sum_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Rollup 1h.
  INSERT INTO requests_win (res, w, service, http_route, http_response_status_code, n, first_ts, last_ts, an, count_sum, count_sumsq, count_min, count_max, count_first, count_last, sum_sum, sum_sumsq, sum_min, sum_max, sum_first, sum_last)
    VALUES (3600, (CAST(NEW.ts AS INTEGER) / 3600 * 3600), NEW.service, NEW.http_route, NEW.http_response_status_code, 1, NEW.ts, NEW.ts, 0, NEW.count, NEW.count * NEW.count, NEW.count, NEW.count, NEW.count, NEW.count, NEW.sum, NEW.sum * NEW.sum, NEW.sum, NEW.sum, NEW.sum, NEW.sum)
    ON CONFLICT (res, w, service, http_route, http_response_status_code) DO UPDATE SET
      n = n + 1,
      count_sum = count_sum + excluded.count_sum,
      count_sumsq = count_sumsq + excluded.count_sumsq,
      count_min = min(count_min, excluded.count_min),
      count_max = max(count_max, excluded.count_max),
      count_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.count_first ELSE count_first END,
      count_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.count_last ELSE count_last END,
      sum_sum = sum_sum + excluded.sum_sum,
      sum_sumsq = sum_sumsq + excluded.sum_sumsq,
      sum_min = min(sum_min, excluded.sum_min),
      sum_max = max(sum_max, excluded.sum_max),
      sum_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.sum_first ELSE sum_first END,
      sum_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.sum_last ELSE sum_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Precomputed answers by service, http_route per day.
  INSERT INTO _pc_requests_by_service_http_route_per_day (service, http_route, period, n, count_sum, sum_sum) VALUES (NEW.service, NEW.http_route, strftime('%Y-%m-%d', NEW.ts, 'unixepoch'), 1, NEW.count, NEW.sum)
    ON CONFLICT (service, http_route, period) DO UPDATE SET n = n + 1, count_sum = count_sum + excluded.count_sum, sum_sum = sum_sum + excluded.sum_sum;
END;
CREATE TRIGGER IF NOT EXISTS queue_ingest INSTEAD OF INSERT ON queue
BEGIN
  SELECT RAISE(ABORT, 'queue: ts, service, depth must not be null') WHERE NEW.ts IS NULL OR NEW.service IS NULL OR NEW.depth IS NULL;
  -- Rollup 1m.
  INSERT INTO queue_win (res, w, service, n, first_ts, last_ts, an, depth_sum, depth_sumsq, depth_min, depth_max, depth_first, depth_last)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.service, 1, NEW.ts, NEW.ts, 0, NEW.depth, NEW.depth * NEW.depth, NEW.depth, NEW.depth, NEW.depth, NEW.depth)
    ON CONFLICT (res, w, service) DO UPDATE SET
      n = n + 1,
      depth_sum = depth_sum + excluded.depth_sum,
      depth_sumsq = depth_sumsq + excluded.depth_sumsq,
      depth_min = min(depth_min, excluded.depth_min),
      depth_max = max(depth_max, excluded.depth_max),
      depth_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.depth_first ELSE depth_first END,
      depth_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.depth_last ELSE depth_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Precomputed answers by service per day.
  INSERT INTO _pc_queue_by_service_per_day (service, period, n, depth_max) VALUES (NEW.service, strftime('%Y-%m-%d', NEW.ts, 'unixepoch'), 1, NEW.depth)
    ON CONFLICT (service, period) DO UPDATE SET n = n + 1, depth_max = max(depth_max, excluded.depth_max);
END;

-- What this file holds, for any tool that opens it.
CREATE TABLE IF NOT EXISTS _precomputing (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing (key, value) VALUES
  ('format', '1'),
  ('compiler', 'precomputing 0.3.0'),
  ('policy', '# OpenTelemetry into Precomputing: an application''s logs and metrics, sent by an OTLP/HTTP
# exporter to precomputing serve (POST /v1/logs and /v1/metrics). Attribute names read with
# their dots as _: http.route is http_route, service.name is service_name (and service).

logs {}                                          # records come with their fields: no line format

stream errors from logs where level = "ERROR" {
  key    service text
  key    template integer                         # the template learned from the body
  raw    keep 2d                                  # every error kept whole for two days
  rollup 1m keep 30d
}

stream requests from metrics "http.server.request.duration" {
  key    service text
  key    http_route text
  key    http_response_status_code integer
  value  count integer                            # requests in the interval
  value  sum real                                 # seconds spent on them
  rollup 1m keep 30d
  rollup 1h keep 1y
}

stream queue from metrics "queue.depth" {
  key    service text
  value  depth real
  rollup 1m keep 7d
}

precompute errors_hour   = count(errors) by service per hour
precompute requests_day  = sum(requests.count) by service, http_route per day
precompute seconds_day   = sum(requests.sum) by service, http_route per day
precompute queue_max_day = max(queue.depth) by service per day
'),
  ('distill', '-- Distill for otel.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream errors.
DELETE FROM errors_raw WHERE ts < :now - 172800;
DELETE FROM errors_win WHERE res = 60 AND w <= :now - 2592060;

-- Stream requests.
DELETE FROM requests_win WHERE res = 60 AND w <= :now - 2592060;
DELETE FROM requests_win WHERE res = 3600 AND w <= :now - 31539600;

-- Stream queue.
DELETE FROM queue_win WHERE res = 60 AND w <= :now - 604860;
');
CREATE TABLE IF NOT EXISTS _precomputing_objects (name TEXT PRIMARY KEY, kind TEXT NOT NULL, stream TEXT NOT NULL, detail TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing_objects (name, kind, stream, detail) VALUES
  ('errors', 'stream', 'errors', '{"derived":[],"from":"logs","insert":"INSERT INTO errors (ts, service, template, line) VALUES (...)","keys":["service","template"],"raw_keep_seconds":172800,"raw_table":"errors_raw","values":[],"where":{"level":"ERROR"}}'),
  ('errors_win', 'windows', 'errors', '{"numbers":[],"rollups":[{"keep_seconds":2592000,"quantiles":[],"res_seconds":60}]}'),
  ('requests', 'stream', 'requests', '{"derived":[],"from":"metrics","insert":"INSERT INTO requests (ts, service, http_route, http_response_status_code, count, sum) VALUES (...)","keys":["service","http_route","http_response_status_code"],"metric":"http.server.request.duration","raw_keep_seconds":0,"raw_table":"requests_raw","values":["count","sum"],"where":{}}'),
  ('requests_win', 'windows', 'requests', '{"numbers":["count","sum"],"rollups":[{"keep_seconds":2592000,"quantiles":[],"res_seconds":60},{"keep_seconds":31536000,"quantiles":[],"res_seconds":3600}]}'),
  ('queue', 'stream', 'queue', '{"derived":[],"from":"metrics","insert":"INSERT INTO queue (ts, service, depth) VALUES (...)","keys":["service"],"metric":"queue.depth","raw_keep_seconds":0,"raw_table":"queue_raw","values":["depth"],"where":{}}'),
  ('queue_win', 'windows', 'queue', '{"numbers":["depth"],"rollups":[{"keep_seconds":604800,"quantiles":[],"res_seconds":60}]}'),
  ('errors_hour', 'precompute', 'errors', '{"by":["service"],"function":"count","per":"hour","state":"_pc_errors_by_service_per_hour","value":null}'),
  ('requests_day', 'precompute', 'requests', '{"by":["service","http_route"],"function":"sum","per":"day","state":"_pc_requests_by_service_http_route_per_day","value":"count"}'),
  ('seconds_day', 'precompute', 'requests', '{"by":["service","http_route"],"function":"sum","per":"day","state":"_pc_requests_by_service_http_route_per_day","value":"sum"}'),
  ('queue_max_day', 'precompute', 'queue', '{"by":["service"],"function":"max","per":"day","state":"_pc_queue_by_service_per_day","value":"depth"}'),
  ('logs', 'logs', '', '{"depth":4,"fields":null,"format":"","masks":[],"similarity":0.5,"templates_table":"_precomputing_templates"}');
