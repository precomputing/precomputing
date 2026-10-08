-- Compiled by Precomputing 0.2.0 from traces.precompute.
-- File format 1. Every statement is safe to run again on the same file.

-- Stream calls. Insert events with:
--   INSERT INTO calls (ts, call_id, repo, run, model, input_tokens, cached_tokens, output_tokens) VALUES (?, ?, ?, ?, ?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS calls_raw (ts INTEGER NOT NULL, call_id TEXT NOT NULL, repo TEXT NOT NULL, run TEXT NOT NULL, model TEXT NOT NULL, input_tokens INTEGER NOT NULL, cached_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS calls_raw_ts ON calls_raw (ts);
CREATE VIEW IF NOT EXISTS calls AS SELECT ts, call_id, repo, run, model, input_tokens, cached_tokens, output_tokens FROM calls_raw;
CREATE TABLE IF NOT EXISTS calls_ids (call_id TEXT NOT NULL PRIMARY KEY, ts INTEGER NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS calls_ids_ts ON calls_ids (ts);
CREATE TABLE IF NOT EXISTS calls_clock (g INTEGER PRIMARY KEY CHECK (g = 1), newest INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS calls_refused (reason TEXT NOT NULL, repo TEXT NOT NULL, run TEXT NOT NULL, model TEXT NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (reason, repo, run, model)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS calls_win (res INTEGER NOT NULL, w INTEGER NOT NULL, repo TEXT NOT NULL, run TEXT NOT NULL, model TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, input_tokens_sum REAL NOT NULL, input_tokens_sumsq REAL NOT NULL, input_tokens_min REAL NOT NULL, input_tokens_max REAL NOT NULL, input_tokens_first REAL NOT NULL, input_tokens_last REAL NOT NULL, cached_tokens_sum REAL NOT NULL, cached_tokens_sumsq REAL NOT NULL, cached_tokens_min REAL NOT NULL, cached_tokens_max REAL NOT NULL, cached_tokens_first REAL NOT NULL, cached_tokens_last REAL NOT NULL, output_tokens_sum REAL NOT NULL, output_tokens_sumsq REAL NOT NULL, output_tokens_min REAL NOT NULL, output_tokens_max REAL NOT NULL, output_tokens_first REAL NOT NULL, output_tokens_last REAL NOT NULL, cost_nano_sum REAL NOT NULL, cost_nano_sumsq REAL NOT NULL, cost_nano_min REAL NOT NULL, cost_nano_max REAL NOT NULL, cost_nano_first REAL NOT NULL, cost_nano_last REAL NOT NULL, PRIMARY KEY (res, w, repo, run, model)) WITHOUT ROWID;

-- Stream context. Insert events with:
--   INSERT INTO context (ts, part_id, repo, source, tokens, cached_tokens, output_tokens) VALUES (?, ?, ?, ?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS context_raw (ts INTEGER NOT NULL, part_id TEXT NOT NULL, repo TEXT NOT NULL, source TEXT NOT NULL, tokens INTEGER NOT NULL, cached_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS context_raw_ts ON context_raw (ts);
CREATE VIEW IF NOT EXISTS context AS SELECT ts, part_id, repo, source, tokens, cached_tokens, output_tokens FROM context_raw;
CREATE TABLE IF NOT EXISTS context_ids (part_id TEXT NOT NULL PRIMARY KEY, ts INTEGER NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS context_ids_ts ON context_ids (ts);
CREATE TABLE IF NOT EXISTS context_clock (g INTEGER PRIMARY KEY CHECK (g = 1), newest INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS context_refused (reason TEXT NOT NULL, repo TEXT NOT NULL, source TEXT NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (reason, repo, source)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS context_win (res INTEGER NOT NULL, w INTEGER NOT NULL, repo TEXT NOT NULL, source TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, tokens_sum REAL NOT NULL, tokens_sumsq REAL NOT NULL, tokens_min REAL NOT NULL, tokens_max REAL NOT NULL, tokens_first REAL NOT NULL, tokens_last REAL NOT NULL, cached_tokens_sum REAL NOT NULL, cached_tokens_sumsq REAL NOT NULL, cached_tokens_min REAL NOT NULL, cached_tokens_max REAL NOT NULL, cached_tokens_first REAL NOT NULL, cached_tokens_last REAL NOT NULL, output_tokens_sum REAL NOT NULL, output_tokens_sumsq REAL NOT NULL, output_tokens_min REAL NOT NULL, output_tokens_max REAL NOT NULL, output_tokens_first REAL NOT NULL, output_tokens_last REAL NOT NULL, cost_nano_sum REAL NOT NULL, cost_nano_sumsq REAL NOT NULL, cost_nano_min REAL NOT NULL, cost_nano_max REAL NOT NULL, cost_nano_first REAL NOT NULL, cost_nano_last REAL NOT NULL, PRIMARY KEY (res, w, repo, source)) WITHOUT ROWID;

-- Precomputed answers from stream calls by run.
CREATE TABLE IF NOT EXISTS _pc_calls_by_run (run TEXT NOT NULL, n INTEGER NOT NULL, input_tokens_sum REAL NOT NULL, cached_tokens_sum REAL NOT NULL, output_tokens_sum REAL NOT NULL, cost_nano_sum REAL NOT NULL, PRIMARY KEY (run)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS run_calls AS SELECT run, n AS value FROM _pc_calls_by_run;
CREATE VIEW IF NOT EXISTS run_input AS SELECT run, input_tokens_sum AS value FROM _pc_calls_by_run;
CREATE VIEW IF NOT EXISTS run_cached AS SELECT run, cached_tokens_sum AS value FROM _pc_calls_by_run;
CREATE VIEW IF NOT EXISTS run_output AS SELECT run, output_tokens_sum AS value FROM _pc_calls_by_run;
CREATE VIEW IF NOT EXISTS run_cost AS SELECT run, cost_nano_sum AS value FROM _pc_calls_by_run;

-- Precomputed answers from stream calls by repo per day.
CREATE TABLE IF NOT EXISTS _pc_calls_by_repo_per_day (repo TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, cost_nano_sum REAL NOT NULL, PRIMARY KEY (repo, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS repo_calls_day AS SELECT repo, period, n AS value FROM _pc_calls_by_repo_per_day;
CREATE VIEW IF NOT EXISTS repo_cost_day AS SELECT repo, period, cost_nano_sum AS value FROM _pc_calls_by_repo_per_day;

-- Precomputed answers from stream calls by model per day.
CREATE TABLE IF NOT EXISTS _pc_calls_by_model_per_day (model TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, cost_nano_sum REAL NOT NULL, PRIMARY KEY (model, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS model_cost_day AS SELECT model, period, cost_nano_sum AS value FROM _pc_calls_by_model_per_day;

-- Precomputed answers from stream context by source per day.
CREATE TABLE IF NOT EXISTS _pc_context_by_source_per_day (source TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, tokens_sum REAL NOT NULL, cost_nano_sum REAL NOT NULL, PRIMARY KEY (source, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS source_tokens_day AS SELECT source, period, tokens_sum AS value FROM _pc_context_by_source_per_day;
CREATE VIEW IF NOT EXISTS source_cost_day AS SELECT source, period, cost_nano_sum AS value FROM _pc_context_by_source_per_day;

-- Precomputed answers from stream context by repo, source per day.
CREATE TABLE IF NOT EXISTS _pc_context_by_repo_source_per_day (repo TEXT NOT NULL, source TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, cost_nano_sum REAL NOT NULL, PRIMARY KEY (repo, source, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS repo_source_cost_day AS SELECT repo, source, period, cost_nano_sum AS value FROM _pc_context_by_repo_source_per_day;

-- Quota repo_budget on repo_cost_day. Put the limits in repo_budget_limit; the view shows each period's use against them.
CREATE TABLE IF NOT EXISTS repo_budget_limit (repo TEXT NOT NULL, lim REAL NOT NULL, PRIMARY KEY (repo)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS repo_budget AS
  SELECT l.repo, u.period, u.value AS used, l.lim, l.lim - u.value AS remaining, u.value >= l.lim AS reached
  FROM repo_budget_limit l JOIN repo_cost_day u USING (repo);

-- The work done on every insert.
CREATE TRIGGER IF NOT EXISTS calls_ingest INSTEAD OF INSERT ON calls
BEGIN
  SELECT RAISE(ABORT, 'calls: ts, call_id, repo, run, model, input_tokens, cached_tokens, output_tokens must not be null') WHERE NEW.ts IS NULL OR NEW.call_id IS NULL OR NEW.repo IS NULL OR NEW.run IS NULL OR NEW.model IS NULL OR NEW.input_tokens IS NULL OR NEW.cached_tokens IS NULL OR NEW.output_tokens IS NULL;
  -- An exact stream refuses what would change a closed period or count an event twice.
  INSERT INTO calls_refused (reason, repo, run, model, n) SELECT 'closed', NEW.repo, NEW.run, NEW.model, 1 WHERE (SELECT newest FROM calls_clock WHERE g = 1) >= ((CAST(NEW.ts AS INTEGER) / 86400 + 1) * 86400) + 86400
    ON CONFLICT (reason, repo, run, model) DO UPDATE SET n = n + 1;
  SELECT RAISE(IGNORE) WHERE (SELECT newest FROM calls_clock WHERE g = 1) >= ((CAST(NEW.ts AS INTEGER) / 86400 + 1) * 86400) + 86400;
  INSERT INTO calls_refused (reason, repo, run, model, n) SELECT 'repeat', NEW.repo, NEW.run, NEW.model, 1 WHERE EXISTS (SELECT 1 FROM calls_ids WHERE call_id = NEW.call_id)
    ON CONFLICT (reason, repo, run, model) DO UPDATE SET n = n + 1;
  SELECT RAISE(IGNORE) WHERE EXISTS (SELECT 1 FROM calls_ids WHERE call_id = NEW.call_id);
  INSERT INTO calls_ids (call_id, ts) VALUES (NEW.call_id, NEW.ts);
  INSERT INTO calls_clock (g, newest) VALUES (1, NEW.ts)
    ON CONFLICT (g) DO UPDATE SET newest = max(newest, excluded.newest);
  INSERT INTO calls_raw (ts, call_id, repo, run, model, input_tokens, cached_tokens, output_tokens) VALUES (NEW.ts, NEW.call_id, NEW.repo, NEW.run, NEW.model, NEW.input_tokens, NEW.cached_tokens, NEW.output_tokens);
  -- Rollup 1h.
  INSERT INTO calls_win (res, w, repo, run, model, n, first_ts, last_ts, an, input_tokens_sum, input_tokens_sumsq, input_tokens_min, input_tokens_max, input_tokens_first, input_tokens_last, cached_tokens_sum, cached_tokens_sumsq, cached_tokens_min, cached_tokens_max, cached_tokens_first, cached_tokens_last, output_tokens_sum, output_tokens_sumsq, output_tokens_min, output_tokens_max, output_tokens_first, output_tokens_last, cost_nano_sum, cost_nano_sumsq, cost_nano_min, cost_nano_max, cost_nano_first, cost_nano_last)
    VALUES (3600, (CAST(NEW.ts AS INTEGER) / 3600 * 3600), NEW.repo, NEW.run, NEW.model, 1, NEW.ts, NEW.ts, 0, NEW.input_tokens, NEW.input_tokens * NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.cached_tokens, NEW.cached_tokens * NEW.cached_tokens, NEW.cached_tokens, NEW.cached_tokens, NEW.cached_tokens, NEW.cached_tokens, NEW.output_tokens, NEW.output_tokens * NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))) * (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))))
    ON CONFLICT (res, w, repo, run, model) DO UPDATE SET
      n = n + 1,
      input_tokens_sum = input_tokens_sum + excluded.input_tokens_sum,
      input_tokens_sumsq = input_tokens_sumsq + excluded.input_tokens_sumsq,
      input_tokens_min = min(input_tokens_min, excluded.input_tokens_min),
      input_tokens_max = max(input_tokens_max, excluded.input_tokens_max),
      input_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.input_tokens_first ELSE input_tokens_first END,
      input_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.input_tokens_last ELSE input_tokens_last END,
      cached_tokens_sum = cached_tokens_sum + excluded.cached_tokens_sum,
      cached_tokens_sumsq = cached_tokens_sumsq + excluded.cached_tokens_sumsq,
      cached_tokens_min = min(cached_tokens_min, excluded.cached_tokens_min),
      cached_tokens_max = max(cached_tokens_max, excluded.cached_tokens_max),
      cached_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.cached_tokens_first ELSE cached_tokens_first END,
      cached_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.cached_tokens_last ELSE cached_tokens_last END,
      output_tokens_sum = output_tokens_sum + excluded.output_tokens_sum,
      output_tokens_sumsq = output_tokens_sumsq + excluded.output_tokens_sumsq,
      output_tokens_min = min(output_tokens_min, excluded.output_tokens_min),
      output_tokens_max = max(output_tokens_max, excluded.output_tokens_max),
      output_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.output_tokens_first ELSE output_tokens_first END,
      output_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.output_tokens_last ELSE output_tokens_last END,
      cost_nano_sum = cost_nano_sum + excluded.cost_nano_sum,
      cost_nano_sumsq = cost_nano_sumsq + excluded.cost_nano_sumsq,
      cost_nano_min = min(cost_nano_min, excluded.cost_nano_min),
      cost_nano_max = max(cost_nano_max, excluded.cost_nano_max),
      cost_nano_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.cost_nano_first ELSE cost_nano_first END,
      cost_nano_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.cost_nano_last ELSE cost_nano_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Rollup 1d.
  INSERT INTO calls_win (res, w, repo, run, model, n, first_ts, last_ts, an, input_tokens_sum, input_tokens_sumsq, input_tokens_min, input_tokens_max, input_tokens_first, input_tokens_last, cached_tokens_sum, cached_tokens_sumsq, cached_tokens_min, cached_tokens_max, cached_tokens_first, cached_tokens_last, output_tokens_sum, output_tokens_sumsq, output_tokens_min, output_tokens_max, output_tokens_first, output_tokens_last, cost_nano_sum, cost_nano_sumsq, cost_nano_min, cost_nano_max, cost_nano_first, cost_nano_last)
    VALUES (86400, (CAST(NEW.ts AS INTEGER) / 86400 * 86400), NEW.repo, NEW.run, NEW.model, 1, NEW.ts, NEW.ts, 0, NEW.input_tokens, NEW.input_tokens * NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.input_tokens, NEW.cached_tokens, NEW.cached_tokens * NEW.cached_tokens, NEW.cached_tokens, NEW.cached_tokens, NEW.cached_tokens, NEW.cached_tokens, NEW.output_tokens, NEW.output_tokens * NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))) * (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))))
    ON CONFLICT (res, w, repo, run, model) DO UPDATE SET
      n = n + 1,
      input_tokens_sum = input_tokens_sum + excluded.input_tokens_sum,
      input_tokens_sumsq = input_tokens_sumsq + excluded.input_tokens_sumsq,
      input_tokens_min = min(input_tokens_min, excluded.input_tokens_min),
      input_tokens_max = max(input_tokens_max, excluded.input_tokens_max),
      input_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.input_tokens_first ELSE input_tokens_first END,
      input_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.input_tokens_last ELSE input_tokens_last END,
      cached_tokens_sum = cached_tokens_sum + excluded.cached_tokens_sum,
      cached_tokens_sumsq = cached_tokens_sumsq + excluded.cached_tokens_sumsq,
      cached_tokens_min = min(cached_tokens_min, excluded.cached_tokens_min),
      cached_tokens_max = max(cached_tokens_max, excluded.cached_tokens_max),
      cached_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.cached_tokens_first ELSE cached_tokens_first END,
      cached_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.cached_tokens_last ELSE cached_tokens_last END,
      output_tokens_sum = output_tokens_sum + excluded.output_tokens_sum,
      output_tokens_sumsq = output_tokens_sumsq + excluded.output_tokens_sumsq,
      output_tokens_min = min(output_tokens_min, excluded.output_tokens_min),
      output_tokens_max = max(output_tokens_max, excluded.output_tokens_max),
      output_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.output_tokens_first ELSE output_tokens_first END,
      output_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.output_tokens_last ELSE output_tokens_last END,
      cost_nano_sum = cost_nano_sum + excluded.cost_nano_sum,
      cost_nano_sumsq = cost_nano_sumsq + excluded.cost_nano_sumsq,
      cost_nano_min = min(cost_nano_min, excluded.cost_nano_min),
      cost_nano_max = max(cost_nano_max, excluded.cost_nano_max),
      cost_nano_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.cost_nano_first ELSE cost_nano_first END,
      cost_nano_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.cost_nano_last ELSE cost_nano_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Precomputed answers by run.
  INSERT INTO _pc_calls_by_run (run, n, input_tokens_sum, cached_tokens_sum, output_tokens_sum, cost_nano_sum) VALUES (NEW.run, 1, NEW.input_tokens, NEW.cached_tokens, NEW.output_tokens, (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))))
    ON CONFLICT (run) DO UPDATE SET n = n + 1, input_tokens_sum = input_tokens_sum + excluded.input_tokens_sum, cached_tokens_sum = cached_tokens_sum + excluded.cached_tokens_sum, output_tokens_sum = output_tokens_sum + excluded.output_tokens_sum, cost_nano_sum = cost_nano_sum + excluded.cost_nano_sum;
  -- Precomputed answers by repo per day.
  INSERT INTO _pc_calls_by_repo_per_day (repo, period, n, cost_nano_sum) VALUES (NEW.repo, strftime('%Y-%m-%d', NEW.ts, 'unixepoch'), 1, (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))))
    ON CONFLICT (repo, period) DO UPDATE SET n = n + 1, cost_nano_sum = cost_nano_sum + excluded.cost_nano_sum;
  -- Precomputed answers by model per day.
  INSERT INTO _pc_calls_by_model_per_day (model, period, n, cost_nano_sum) VALUES (NEW.model, strftime('%Y-%m-%d', NEW.ts, 'unixepoch'), 1, (((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))))
    ON CONFLICT (model, period) DO UPDATE SET n = n + 1, cost_nano_sum = cost_nano_sum + excluded.cost_nano_sum;
END;
CREATE TRIGGER IF NOT EXISTS context_ingest INSTEAD OF INSERT ON context
BEGIN
  SELECT RAISE(ABORT, 'context: ts, part_id, repo, source, tokens, cached_tokens, output_tokens must not be null') WHERE NEW.ts IS NULL OR NEW.part_id IS NULL OR NEW.repo IS NULL OR NEW.source IS NULL OR NEW.tokens IS NULL OR NEW.cached_tokens IS NULL OR NEW.output_tokens IS NULL;
  -- An exact stream refuses what would change a closed period or count an event twice.
  INSERT INTO context_refused (reason, repo, source, n) SELECT 'closed', NEW.repo, NEW.source, 1 WHERE (SELECT newest FROM context_clock WHERE g = 1) >= ((CAST(NEW.ts AS INTEGER) / 86400 + 1) * 86400) + 86400
    ON CONFLICT (reason, repo, source) DO UPDATE SET n = n + 1;
  SELECT RAISE(IGNORE) WHERE (SELECT newest FROM context_clock WHERE g = 1) >= ((CAST(NEW.ts AS INTEGER) / 86400 + 1) * 86400) + 86400;
  INSERT INTO context_refused (reason, repo, source, n) SELECT 'repeat', NEW.repo, NEW.source, 1 WHERE EXISTS (SELECT 1 FROM context_ids WHERE part_id = NEW.part_id)
    ON CONFLICT (reason, repo, source) DO UPDATE SET n = n + 1;
  SELECT RAISE(IGNORE) WHERE EXISTS (SELECT 1 FROM context_ids WHERE part_id = NEW.part_id);
  INSERT INTO context_ids (part_id, ts) VALUES (NEW.part_id, NEW.ts);
  INSERT INTO context_clock (g, newest) VALUES (1, NEW.ts)
    ON CONFLICT (g) DO UPDATE SET newest = max(newest, excluded.newest);
  INSERT INTO context_raw (ts, part_id, repo, source, tokens, cached_tokens, output_tokens) VALUES (NEW.ts, NEW.part_id, NEW.repo, NEW.source, NEW.tokens, NEW.cached_tokens, NEW.output_tokens);
  -- Rollup 1h.
  INSERT INTO context_win (res, w, repo, source, n, first_ts, last_ts, an, tokens_sum, tokens_sumsq, tokens_min, tokens_max, tokens_first, tokens_last, cached_tokens_sum, cached_tokens_sumsq, cached_tokens_min, cached_tokens_max, cached_tokens_first, cached_tokens_last, output_tokens_sum, output_tokens_sumsq, output_tokens_min, output_tokens_max, output_tokens_first, output_tokens_last, cost_nano_sum, cost_nano_sumsq, cost_nano_min, cost_nano_max, cost_nano_first, cost_nano_last)
    VALUES (3600, (CAST(NEW.ts AS INTEGER) / 3600 * 3600), NEW.repo, NEW.source, 1, NEW.ts, NEW.ts, 0, NEW.tokens, NEW.tokens * NEW.tokens, NEW.tokens, NEW.tokens, NEW.tokens, NEW.tokens, NEW.cached_tokens, NEW.cached_tokens * NEW.cached_tokens, NEW.cached_tokens, NEW.cached_tokens, NEW.cached_tokens, NEW.cached_tokens, NEW.output_tokens, NEW.output_tokens * NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, NEW.output_tokens, (((((NEW.tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))) * (((((NEW.tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))), (((((NEW.tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))))
    ON CONFLICT (res, w, repo, source) DO UPDATE SET
      n = n + 1,
      tokens_sum = tokens_sum + excluded.tokens_sum,
      tokens_sumsq = tokens_sumsq + excluded.tokens_sumsq,
      tokens_min = min(tokens_min, excluded.tokens_min),
      tokens_max = max(tokens_max, excluded.tokens_max),
      tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.tokens_first ELSE tokens_first END,
      tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.tokens_last ELSE tokens_last END,
      cached_tokens_sum = cached_tokens_sum + excluded.cached_tokens_sum,
      cached_tokens_sumsq = cached_tokens_sumsq + excluded.cached_tokens_sumsq,
      cached_tokens_min = min(cached_tokens_min, excluded.cached_tokens_min),
      cached_tokens_max = max(cached_tokens_max, excluded.cached_tokens_max),
      cached_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.cached_tokens_first ELSE cached_tokens_first END,
      cached_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.cached_tokens_last ELSE cached_tokens_last END,
      output_tokens_sum = output_tokens_sum + excluded.output_tokens_sum,
      output_tokens_sumsq = output_tokens_sumsq + excluded.output_tokens_sumsq,
      output_tokens_min = min(output_tokens_min, excluded.output_tokens_min),
      output_tokens_max = max(output_tokens_max, excluded.output_tokens_max),
      output_tokens_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.output_tokens_first ELSE output_tokens_first END,
      output_tokens_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.output_tokens_last ELSE output_tokens_last END,
      cost_nano_sum = cost_nano_sum + excluded.cost_nano_sum,
      cost_nano_sumsq = cost_nano_sumsq + excluded.cost_nano_sumsq,
      cost_nano_min = min(cost_nano_min, excluded.cost_nano_min),
      cost_nano_max = max(cost_nano_max, excluded.cost_nano_max),
      cost_nano_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.cost_nano_first ELSE cost_nano_first END,
      cost_nano_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.cost_nano_last ELSE cost_nano_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Precomputed answers by source per day.
  INSERT INTO _pc_context_by_source_per_day (source, period, n, tokens_sum, cost_nano_sum) VALUES (NEW.source, strftime('%Y-%m-%d', NEW.ts, 'unixepoch'), 1, NEW.tokens, (((((NEW.tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))))
    ON CONFLICT (source, period) DO UPDATE SET n = n + 1, tokens_sum = tokens_sum + excluded.tokens_sum, cost_nano_sum = cost_nano_sum + excluded.cost_nano_sum;
  -- Precomputed answers by repo, source per day.
  INSERT INTO _pc_context_by_repo_source_per_day (repo, source, period, n, cost_nano_sum) VALUES (NEW.repo, NEW.source, strftime('%Y-%m-%d', NEW.ts, 'unixepoch'), 1, (((((NEW.tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))))
    ON CONFLICT (repo, source, period) DO UPDATE SET n = n + 1, cost_nano_sum = cost_nano_sum + excluded.cost_nano_sum;
END;

-- What this file holds, for any tool that opens it.
CREATE TABLE IF NOT EXISTS _precomputing (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing (key, value) VALUES
  ('format', '1'),
  ('compiler', 'precomputing 0.2.0'),
  ('policy', '# Agent traces, metered by the call. Every model call an AI agent makes is counted once, with its
# tokens and its cost at example prices, and each call''s input is split by where it came from:
# the system prompt, the tool list, the task, the agent''s earlier messages, or a tool''s output.
#
# Prices, in billionths of a dollar per token (examples, not a provider''s price list):
# 400 for fresh input, 40 for input the provider serves from its prompt cache, 1,600 for output.

stream calls exact {
  id     call_id refuse repeats 7d            # a call reported twice counts once
  key    repo text                            # the repository the agent works on
  key    run text                             # the agent run
  key    model text
  value  input_tokens integer
  value  cached_tokens integer                # input the provider had cached from the run''s last call
  value  output_tokens integer
  derive cost_nano = (input_tokens - cached_tokens) * 400 + cached_tokens * 40 + output_tokens * 1600

  period day close 24h                        # a day closes a day after it ends
  raw    until closed + 30d                   # every call kept whole for 30 days after its day closes
  rollup 1h keep 400d
  rollup 1d keep forever
}

stream context exact {
  id     part_id refuse repeats 7d            # one event per call and source
  key    repo text
  key    source text                          # system, tools, task, assistant, output, or tool:NAME
  value  tokens integer                       # input tokens from this source
  value  cached_tokens integer
  value  output_tokens integer                # the reply, for the source output
  derive cost_nano = (tokens - cached_tokens) * 400 + cached_tokens * 40 + output_tokens * 1600

  period day close 24h
  raw    until closed + 30d
  rollup 1h keep 400d
}

precompute run_calls   = count(calls) by run
precompute run_input   = sum(calls.input_tokens) by run
precompute run_cached  = sum(calls.cached_tokens) by run
precompute run_output  = sum(calls.output_tokens) by run
precompute run_cost    = sum(calls.cost_nano) by run

precompute repo_calls_day = count(calls) by repo per day
precompute repo_cost_day  = sum(calls.cost_nano) by repo per day
quota repo_budget = repo_cost_day                 # a budget per repository per day

precompute model_cost_day = sum(calls.cost_nano) by model per day

precompute source_tokens_day = sum(context.tokens) by source per day
precompute source_cost_day   = sum(context.cost_nano) by source per day
precompute repo_source_cost_day = sum(context.cost_nano) by repo, source per day
'),
  ('distill', '-- Distill for traces.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream calls.
DELETE FROM calls_raw WHERE ts < (CAST(:now - 2678400 AS INTEGER) / 86400 * 86400);
DELETE FROM calls_ids WHERE ts < :now - 604800;
DELETE FROM calls_win WHERE res = 3600 AND w <= :now - 34563600;

-- Stream context.
DELETE FROM context_raw WHERE ts < (CAST(:now - 2678400 AS INTEGER) / 86400 * 86400);
DELETE FROM context_ids WHERE ts < :now - 604800;
DELETE FROM context_win WHERE res = 3600 AND w <= :now - 34563600;
');
CREATE TABLE IF NOT EXISTS _precomputing_objects (name TEXT PRIMARY KEY, kind TEXT NOT NULL, stream TEXT NOT NULL, detail TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing_objects (name, kind, stream, detail) VALUES
  ('calls', 'stream', 'calls', '{"close_seconds":86400,"derived":[{"name":"cost_nano","sql":"((((NEW.input_tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))"}],"exact":true,"id":"call_id","ids_table":"calls_ids","insert":"INSERT INTO calls (ts, call_id, repo, run, model, input_tokens, cached_tokens, output_tokens) VALUES (...)","keys":["repo","run","model"],"late_seconds":0,"period":"day","raw_after_close_seconds":2592000,"raw_keep_seconds":0,"raw_table":"calls_raw","refused_table":"calls_refused","repeats_seconds":604800,"values":["input_tokens","cached_tokens","output_tokens"]}'),
  ('calls_win', 'windows', 'calls', '{"numbers":["input_tokens","cached_tokens","output_tokens","cost_nano"],"rollups":[{"keep_seconds":34560000,"quantiles":[],"res_seconds":3600},{"keep_seconds":-1,"quantiles":[],"res_seconds":86400}]}'),
  ('context', 'stream', 'context', '{"close_seconds":86400,"derived":[{"name":"cost_nano","sql":"((((NEW.tokens - NEW.cached_tokens) * 400) + (NEW.cached_tokens * 40)) + (NEW.output_tokens * 1600))"}],"exact":true,"id":"part_id","ids_table":"context_ids","insert":"INSERT INTO context (ts, part_id, repo, source, tokens, cached_tokens, output_tokens) VALUES (...)","keys":["repo","source"],"late_seconds":0,"period":"day","raw_after_close_seconds":2592000,"raw_keep_seconds":0,"raw_table":"context_raw","refused_table":"context_refused","repeats_seconds":604800,"values":["tokens","cached_tokens","output_tokens"]}'),
  ('context_win', 'windows', 'context', '{"numbers":["tokens","cached_tokens","output_tokens","cost_nano"],"rollups":[{"keep_seconds":34560000,"quantiles":[],"res_seconds":3600}]}'),
  ('run_calls', 'precompute', 'calls', '{"by":["run"],"function":"count","per":null,"state":"_pc_calls_by_run","value":null}'),
  ('run_input', 'precompute', 'calls', '{"by":["run"],"function":"sum","per":null,"state":"_pc_calls_by_run","value":"input_tokens"}'),
  ('run_cached', 'precompute', 'calls', '{"by":["run"],"function":"sum","per":null,"state":"_pc_calls_by_run","value":"cached_tokens"}'),
  ('run_output', 'precompute', 'calls', '{"by":["run"],"function":"sum","per":null,"state":"_pc_calls_by_run","value":"output_tokens"}'),
  ('run_cost', 'precompute', 'calls', '{"by":["run"],"function":"sum","per":null,"state":"_pc_calls_by_run","value":"cost_nano"}'),
  ('repo_calls_day', 'precompute', 'calls', '{"by":["repo"],"function":"count","per":"day","state":"_pc_calls_by_repo_per_day","value":null}'),
  ('repo_cost_day', 'precompute', 'calls', '{"by":["repo"],"function":"sum","per":"day","state":"_pc_calls_by_repo_per_day","value":"cost_nano"}'),
  ('model_cost_day', 'precompute', 'calls', '{"by":["model"],"function":"sum","per":"day","state":"_pc_calls_by_model_per_day","value":"cost_nano"}'),
  ('source_tokens_day', 'precompute', 'context', '{"by":["source"],"function":"sum","per":"day","state":"_pc_context_by_source_per_day","value":"tokens"}'),
  ('source_cost_day', 'precompute', 'context', '{"by":["source"],"function":"sum","per":"day","state":"_pc_context_by_source_per_day","value":"cost_nano"}'),
  ('repo_source_cost_day', 'precompute', 'context', '{"by":["repo","source"],"function":"sum","per":"day","state":"_pc_context_by_repo_source_per_day","value":"cost_nano"}'),
  ('repo_budget', 'quota', 'calls', '{"by":["repo"],"limit_table":"repo_budget_limit","per":"day","precompute":"repo_cost_day"}');
