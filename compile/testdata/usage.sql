-- Compiled by Precomputing 0.1.1 from usage.precompute.
-- File format 1. Every statement is safe to run again on the same file.

-- Stream usage. Insert events with:
--   INSERT INTO usage (ts, request_id, customer, model, gateway, input_tokens, output_tokens) VALUES (?, ?, ?, ?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS usage_raw (ts INTEGER NOT NULL, request_id TEXT NOT NULL, customer TEXT NOT NULL, model TEXT NOT NULL, gateway TEXT NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS usage_raw_ts ON usage_raw (ts);
CREATE VIEW IF NOT EXISTS usage AS SELECT ts, request_id, customer, model, gateway, input_tokens, output_tokens FROM usage_raw;
CREATE TABLE IF NOT EXISTS usage_ids (request_id TEXT NOT NULL PRIMARY KEY, ts INTEGER NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS usage_ids_ts ON usage_ids (ts);
CREATE TABLE IF NOT EXISTS usage_clock (g INTEGER PRIMARY KEY CHECK (g = 1), newest INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS usage_refused (reason TEXT NOT NULL, customer TEXT NOT NULL, model TEXT NOT NULL, gateway TEXT NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (reason, customer, model, gateway)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS usage_win (res INTEGER NOT NULL, w INTEGER NOT NULL, customer TEXT NOT NULL, model TEXT NOT NULL, gateway TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, input_tokens_sum REAL NOT NULL, input_tokens_sumsq REAL NOT NULL, input_tokens_min REAL NOT NULL, input_tokens_max REAL NOT NULL, input_tokens_first REAL NOT NULL, input_tokens_last REAL NOT NULL, output_tokens_sum REAL NOT NULL, output_tokens_sumsq REAL NOT NULL, output_tokens_min REAL NOT NULL, output_tokens_max REAL NOT NULL, output_tokens_first REAL NOT NULL, output_tokens_last REAL NOT NULL, tokens_sum REAL NOT NULL, tokens_sumsq REAL NOT NULL, tokens_min REAL NOT NULL, tokens_max REAL NOT NULL, tokens_first REAL NOT NULL, tokens_last REAL NOT NULL, PRIMARY KEY (res, w, customer, model, gateway)) WITHOUT ROWID;

-- Precomputed answers from stream usage by customer, model per month.
CREATE TABLE IF NOT EXISTS _pc_usage_by_customer_model_per_month (customer TEXT NOT NULL, model TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, input_tokens_sum REAL NOT NULL, output_tokens_sum REAL NOT NULL, PRIMARY KEY (customer, model, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS requests_month AS SELECT customer, model, period, n AS value FROM _pc_usage_by_customer_model_per_month;
CREATE VIEW IF NOT EXISTS input_month AS SELECT customer, model, period, input_tokens_sum AS value FROM _pc_usage_by_customer_model_per_month;
CREATE VIEW IF NOT EXISTS output_month AS SELECT customer, model, period, output_tokens_sum AS value FROM _pc_usage_by_customer_model_per_month;

-- Precomputed answers from stream usage by customer per month.
CREATE TABLE IF NOT EXISTS _pc_usage_by_customer_per_month (customer TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, tokens_sum REAL NOT NULL, PRIMARY KEY (customer, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS tokens_month AS SELECT customer, period, tokens_sum AS value FROM _pc_usage_by_customer_per_month;

-- Quota monthly_tokens on tokens_month. Put the limits in monthly_tokens_limit; the view shows each period's use against them.
CREATE TABLE IF NOT EXISTS monthly_tokens_limit (customer TEXT NOT NULL, lim REAL NOT NULL, PRIMARY KEY (customer)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS monthly_tokens AS
  SELECT l.customer, u.period, u.value AS used, l.lim, l.lim - u.value AS remaining, u.value >= l.lim AS reached
  FROM monthly_tokens_limit l JOIN tokens_month u USING (customer);

-- The work done on every insert.
CREATE TRIGGER IF NOT EXISTS usage_ingest INSTEAD OF INSERT ON usage
BEGIN
  SELECT RAISE(ABORT, 'usage: ts, request_id, customer, model, gateway, input_tokens, output_tokens must not be null') WHERE NEW.ts IS NULL OR NEW.request_id IS NULL OR NEW.customer IS NULL OR NEW.model IS NULL OR NEW.gateway IS NULL OR NEW.input_tokens IS NULL OR NEW.output_tokens IS NULL;
  -- An exact stream refuses what would change a closed period or count an event twice.
  INSERT INTO usage_refused (reason, customer, model, gateway, n) SELECT 'late', NEW.customer, NEW.model, NEW.gateway, 1 WHERE NEW.ts < (SELECT newest FROM usage_clock WHERE g = 1) - 172800
    ON CONFLICT (reason, customer, model, gateway) DO UPDATE SET n = n + 1;
  SELECT RAISE(IGNORE) WHERE NEW.ts < (SELECT newest FROM usage_clock WHERE g = 1) - 172800;
  INSERT INTO usage_refused (reason, customer, model, gateway, n) SELECT 'closed', NEW.customer, NEW.model, NEW.gateway, 1 WHERE (SELECT newest FROM usage_clock WHERE g = 1) >= CAST(strftime('%s', NEW.ts, 'unixepoch', 'start of month', '+1 month') AS INTEGER) + 86400
    ON CONFLICT (reason, customer, model, gateway) DO UPDATE SET n = n + 1;
  SELECT RAISE(IGNORE) WHERE (SELECT newest FROM usage_clock WHERE g = 1) >= CAST(strftime('%s', NEW.ts, 'unixepoch', 'start of month', '+1 month') AS INTEGER) + 86400;
  INSERT INTO usage_refused (reason, customer, model, gateway, n) SELECT 'repeat', NEW.customer, NEW.model, NEW.gateway, 1 WHERE EXISTS (SELECT 1 FROM usage_ids WHERE request_id = NEW.request_id)
    ON CONFLICT (reason, customer, model, gateway) DO UPDATE SET n = n + 1;
  SELECT RAISE(IGNORE) WHERE EXISTS (SELECT 1 FROM usage_ids WHERE request_id = NEW.request_id);
  INSERT INTO usage_ids (request_id, ts) VALUES (NEW.request_id, NEW.ts);
  INSERT INTO usage_clock (g, newest) VALUES (1, NEW.ts)
    ON CONFLICT (g) DO UPDATE SET newest = max(newest, excluded.newest);
  INSERT INTO usage_raw (ts, request_id, customer, model, gateway, input_tokens, output_tokens) VALUES (NEW.ts, NEW.request_id, NEW.customer, NEW.model, NEW.gateway, NEW.input_tokens, NEW.output_tokens);
  -- Rollup 1h.
  INSERT INTO usage_win (res, w, customer, model, gateway, n, first_ts, last_ts, an, input_tokens_sum, input_tokens_sumsq, input_tokens_min, input_tokens_max, input_tokens_first, input_tokens_last, output_tokens_sum, output_tokens_sumsq, output_tokens_min, output_tokens_max, output_tokens_first, output_tokens_last, tokens_sum, tokens_sumsq, tokens_min, tokens_max, tokens_first, tokens_last)
    VALUES (3600, (CAST(NEW.ts AS INTEGER) / 3600 * 3600), NEW.customer, NEW.model, NEW.gateway, 1, NEW.ts, NEW.ts, 0, NEW.input_tokens, NEW.input_tokens * NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.output_tokens, NEW.output_tokens * NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, ((NEW.input_tokens + NEW.output_tokens)), ((NEW.input_tokens + NEW.output_tokens)) * ((NEW.input_tokens + NEW.output_tokens)), ((NEW.input_tokens + NEW.output_tokens)), ((NEW.input_tokens + NEW.output_tokens)), ((NEW.input_tokens + NEW.output_tokens)), ((NEW.input_tokens + NEW.output_tokens)))
    ON CONFLICT (res, w, customer, model, gateway) DO UPDATE SET
      n = n + 1,
      input_tokens_sum = input_tokens_sum + excluded.input_tokens_sum,
      input_tokens_sumsq = input_tokens_sumsq + excluded.input_tokens_sumsq,
      input_tokens_min = min(input_tokens_min, excluded.input_tokens_min),
      input_tokens_max = max(input_tokens_max, excluded.input_tokens_max),
      input_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.input_tokens_first ELSE input_tokens_first END,
      input_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.input_tokens_last ELSE input_tokens_last END,
      output_tokens_sum = output_tokens_sum + excluded.output_tokens_sum,
      output_tokens_sumsq = output_tokens_sumsq + excluded.output_tokens_sumsq,
      output_tokens_min = min(output_tokens_min, excluded.output_tokens_min),
      output_tokens_max = max(output_tokens_max, excluded.output_tokens_max),
      output_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.output_tokens_first ELSE output_tokens_first END,
      output_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.output_tokens_last ELSE output_tokens_last END,
      tokens_sum = tokens_sum + excluded.tokens_sum,
      tokens_sumsq = tokens_sumsq + excluded.tokens_sumsq,
      tokens_min = min(tokens_min, excluded.tokens_min),
      tokens_max = max(tokens_max, excluded.tokens_max),
      tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.tokens_first ELSE tokens_first END,
      tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.tokens_last ELSE tokens_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Rollup 1d.
  INSERT INTO usage_win (res, w, customer, model, gateway, n, first_ts, last_ts, an, input_tokens_sum, input_tokens_sumsq, input_tokens_min, input_tokens_max, input_tokens_first, input_tokens_last, output_tokens_sum, output_tokens_sumsq, output_tokens_min, output_tokens_max, output_tokens_first, output_tokens_last, tokens_sum, tokens_sumsq, tokens_min, tokens_max, tokens_first, tokens_last)
    VALUES (86400, (CAST(NEW.ts AS INTEGER) / 86400 * 86400), NEW.customer, NEW.model, NEW.gateway, 1, NEW.ts, NEW.ts, 0, NEW.input_tokens, NEW.input_tokens * NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.output_tokens, NEW.output_tokens * NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, ((NEW.input_tokens + NEW.output_tokens)), ((NEW.input_tokens + NEW.output_tokens)) * ((NEW.input_tokens + NEW.output_tokens)), ((NEW.input_tokens + NEW.output_tokens)), ((NEW.input_tokens + NEW.output_tokens)), ((NEW.input_tokens + NEW.output_tokens)), ((NEW.input_tokens + NEW.output_tokens)))
    ON CONFLICT (res, w, customer, model, gateway) DO UPDATE SET
      n = n + 1,
      input_tokens_sum = input_tokens_sum + excluded.input_tokens_sum,
      input_tokens_sumsq = input_tokens_sumsq + excluded.input_tokens_sumsq,
      input_tokens_min = min(input_tokens_min, excluded.input_tokens_min),
      input_tokens_max = max(input_tokens_max, excluded.input_tokens_max),
      input_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.input_tokens_first ELSE input_tokens_first END,
      input_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.input_tokens_last ELSE input_tokens_last END,
      output_tokens_sum = output_tokens_sum + excluded.output_tokens_sum,
      output_tokens_sumsq = output_tokens_sumsq + excluded.output_tokens_sumsq,
      output_tokens_min = min(output_tokens_min, excluded.output_tokens_min),
      output_tokens_max = max(output_tokens_max, excluded.output_tokens_max),
      output_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.output_tokens_first ELSE output_tokens_first END,
      output_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.output_tokens_last ELSE output_tokens_last END,
      tokens_sum = tokens_sum + excluded.tokens_sum,
      tokens_sumsq = tokens_sumsq + excluded.tokens_sumsq,
      tokens_min = min(tokens_min, excluded.tokens_min),
      tokens_max = max(tokens_max, excluded.tokens_max),
      tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.tokens_first ELSE tokens_first END,
      tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.tokens_last ELSE tokens_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Precomputed answers by customer, model per month.
  INSERT INTO _pc_usage_by_customer_model_per_month (customer, model, period, n, input_tokens_sum, output_tokens_sum) VALUES (NEW.customer, NEW.model, strftime('%Y-%m', NEW.ts, 'unixepoch'), 1, NEW.input_tokens, NEW.output_tokens)
    ON CONFLICT (customer, model, period) DO UPDATE SET n = n + 1, input_tokens_sum = input_tokens_sum + excluded.input_tokens_sum, output_tokens_sum = output_tokens_sum + excluded.output_tokens_sum;
  -- Precomputed answers by customer per month.
  INSERT INTO _pc_usage_by_customer_per_month (customer, period, n, tokens_sum) VALUES (NEW.customer, strftime('%Y-%m', NEW.ts, 'unixepoch'), 1, ((NEW.input_tokens + NEW.output_tokens)))
    ON CONFLICT (customer, period) DO UPDATE SET n = n + 1, tokens_sum = tokens_sum + excluded.tokens_sum;
END;

-- What this file holds, for any tool that opens it.
CREATE TABLE IF NOT EXISTS _precomputing (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing (key, value) VALUES
  ('format', '1'),
  ('compiler', 'precomputing 0.1.1'),
  ('policy', '# AI usage metering: the policy behind Demo 3.
# Every request to a model is one event, sent by the gateway that carried it.
# Invoice lines are precomputes; a month closes a day after it ends.

stream usage exact {
  id     request_id refuse repeats 7d     # a retried request is counted once
  key    customer text
  key    model text
  key    gateway text
  value  input_tokens integer
  value  output_tokens integer
  derive tokens = input_tokens + output_tokens

  late   48h                              # up to two days late still counts, in the month it happened,
  period month close 24h                  # unless that month has closed: a month closes a day after it ends
  raw    until closed + 90d               # every request kept until its month has been closed 90 days
  rollup 1h keep 400d
  rollup 1d keep forever
}

# Invoice lines: per customer and model, per month.
precompute requests_month = count(usage) by customer, model per month
precompute input_month    = sum(usage.input_tokens) by customer, model per month
precompute output_month   = sum(usage.output_tokens) by customer, model per month

# What each customer''s plan limits: tokens a month.
precompute tokens_month   = sum(usage.tokens) by customer per month
quota monthly_tokens = tokens_month
'),
  ('distill', '-- Distill for usage.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream usage.
DELETE FROM usage_raw WHERE ts < CAST(strftime(''%s'', :now - 7862400, ''unixepoch'', ''start of month'') AS INTEGER);
DELETE FROM usage_ids WHERE ts < :now - 604800;
DELETE FROM usage_win WHERE res = 3600 AND w <= :now - 34563600;
');
CREATE TABLE IF NOT EXISTS _precomputing_objects (name TEXT PRIMARY KEY, kind TEXT NOT NULL, stream TEXT NOT NULL, detail TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing_objects (name, kind, stream, detail) VALUES
  ('usage', 'stream', 'usage', '{"close_seconds":86400,"derived":[{"name":"tokens","sql":"(NEW.input_tokens + NEW.output_tokens)"}],"exact":true,"id":"request_id","ids_table":"usage_ids","insert":"INSERT INTO usage (ts, request_id, customer, model, gateway, input_tokens, output_tokens) VALUES (...)","keys":["customer","model","gateway"],"late_seconds":172800,"period":"month","raw_after_close_seconds":7776000,"raw_keep_seconds":0,"raw_table":"usage_raw","refused_table":"usage_refused","repeats_seconds":604800,"values":["input_tokens","output_tokens"]}'),
  ('usage_win', 'windows', 'usage', '{"numbers":["input_tokens","output_tokens","tokens"],"rollups":[{"keep_seconds":34560000,"quantiles":[],"res_seconds":3600},{"keep_seconds":-1,"quantiles":[],"res_seconds":86400}]}'),
  ('requests_month', 'precompute', 'usage', '{"by":["customer","model"],"function":"count","per":"month","state":"_pc_usage_by_customer_model_per_month","value":null}'),
  ('input_month', 'precompute', 'usage', '{"by":["customer","model"],"function":"sum","per":"month","state":"_pc_usage_by_customer_model_per_month","value":"input_tokens"}'),
  ('output_month', 'precompute', 'usage', '{"by":["customer","model"],"function":"sum","per":"month","state":"_pc_usage_by_customer_model_per_month","value":"output_tokens"}'),
  ('tokens_month', 'precompute', 'usage', '{"by":["customer"],"function":"sum","per":"month","state":"_pc_usage_by_customer_per_month","value":"tokens"}'),
  ('monthly_tokens', 'quota', 'usage', '{"by":["customer"],"limit_table":"monthly_tokens_limit","per":"month","precompute":"tokens_month"}');
