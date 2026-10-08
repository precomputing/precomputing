-- Compiled by Precomputing 0.2.0 from trades.precompute.
-- File format 1. Every statement is safe to run again on the same file.

-- Stream trades. Insert events with:
--   INSERT INTO trades (ts, symbol, price, size) VALUES (?, ?, ?, ?);
CREATE TABLE IF NOT EXISTS trades_raw (ts INTEGER NOT NULL, symbol TEXT NOT NULL, price REAL NOT NULL, size INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS trades_raw_ts ON trades_raw (ts);
CREATE VIEW IF NOT EXISTS trades AS SELECT ts, symbol, price, size FROM trades_raw;
CREATE TABLE IF NOT EXISTS trades_win (res INTEGER NOT NULL, w INTEGER NOT NULL, symbol TEXT NOT NULL, n INTEGER NOT NULL, first_ts INTEGER NOT NULL, last_ts INTEGER NOT NULL, an INTEGER NOT NULL, price_sum REAL NOT NULL, price_sumsq REAL NOT NULL, price_min REAL NOT NULL, price_max REAL NOT NULL, price_first REAL NOT NULL, price_last REAL NOT NULL, size_sum REAL NOT NULL, size_sumsq REAL NOT NULL, size_min REAL NOT NULL, size_max REAL NOT NULL, size_first REAL NOT NULL, size_last REAL NOT NULL, notional_sum REAL NOT NULL, notional_sumsq REAL NOT NULL, notional_min REAL NOT NULL, notional_max REAL NOT NULL, notional_first REAL NOT NULL, notional_last REAL NOT NULL, PRIMARY KEY (res, w, symbol)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS trades_base (symbol TEXT NOT NULL, n INTEGER NOT NULL, m REAL NOT NULL, var REAL NOT NULL, prev REAL NOT NULL, PRIMARY KEY (symbol)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS trades_anomaly (ts INTEGER NOT NULL, symbol TEXT NOT NULL, price REAL NOT NULL, size INTEGER NOT NULL, z REAL);

-- Precomputed answers from stream trades by symbol.
CREATE TABLE IF NOT EXISTS _pc_trades_by_symbol (symbol TEXT NOT NULL, n INTEGER NOT NULL, price_last REAL NOT NULL, last_ts INTEGER NOT NULL, PRIMARY KEY (symbol)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS last_price AS SELECT symbol, price_last AS value FROM _pc_trades_by_symbol;

-- Precomputed answers from stream trades by symbol per day.
CREATE TABLE IF NOT EXISTS _pc_trades_by_symbol_per_day (symbol TEXT NOT NULL, period TEXT NOT NULL, n INTEGER NOT NULL, size_sum REAL NOT NULL, notional_sum REAL NOT NULL, price_min REAL NOT NULL, price_max REAL NOT NULL, price_first REAL NOT NULL, first_ts INTEGER NOT NULL, PRIMARY KEY (symbol, period)) WITHOUT ROWID;
CREATE VIEW IF NOT EXISTS day_open AS SELECT symbol, period, price_first AS value FROM _pc_trades_by_symbol_per_day;
CREATE VIEW IF NOT EXISTS day_high AS SELECT symbol, period, price_max AS value FROM _pc_trades_by_symbol_per_day;
CREATE VIEW IF NOT EXISTS day_low AS SELECT symbol, period, price_min AS value FROM _pc_trades_by_symbol_per_day;
CREATE VIEW IF NOT EXISTS day_volume AS SELECT symbol, period, size_sum AS value FROM _pc_trades_by_symbol_per_day;
CREATE VIEW IF NOT EXISTS day_turnover AS SELECT symbol, period, notional_sum AS value FROM _pc_trades_by_symbol_per_day;
CREATE VIEW IF NOT EXISTS day_trades AS SELECT symbol, period, n AS value FROM _pc_trades_by_symbol_per_day;

-- Precomputed answers from stream trades.
CREATE TABLE IF NOT EXISTS _pc_trades (g INTEGER PRIMARY KEY CHECK (g = 1), n INTEGER NOT NULL);
CREATE VIEW IF NOT EXISTS trades_total AS SELECT n AS value FROM _pc_trades;

-- The work done on every insert.
CREATE TRIGGER IF NOT EXISTS trades_ingest INSTEAD OF INSERT ON trades
BEGIN
  SELECT RAISE(ABORT, 'trades: ts, symbol, price, size must not be null') WHERE NEW.ts IS NULL OR NEW.symbol IS NULL OR NEW.price IS NULL OR NEW.size IS NULL;
  INSERT INTO trades_raw (ts, symbol, price, size) VALUES (NEW.ts, NEW.symbol, NEW.price, NEW.size);
  -- An unusual event is kept whole, judged against the baseline before this event updates it.
  INSERT INTO trades_anomaly (ts, symbol, price, size, z)
    SELECT NEW.ts, NEW.symbol, NEW.price, NEW.size, ((ln(NEW.price) - prev) - m) / sqrt(var) FROM trades_base
    WHERE symbol = NEW.symbol AND NEW.price > 0 AND n >= 200 AND ((ln(NEW.price) - prev) - m) * ((ln(NEW.price) - prev) - m) > 36 * var
      AND coalesce((SELECT an FROM trades_win WHERE res = 60 AND w = (CAST(NEW.ts AS INTEGER) / 60 * 60) AND trades_win.symbol = NEW.symbol), 0) < 5;
  -- Rollup 1s.
  INSERT INTO trades_win (res, w, symbol, n, first_ts, last_ts, an, price_sum, price_sumsq, price_min, price_max, price_first, price_last, size_sum, size_sumsq, size_min, size_max, size_first, size_last, notional_sum, notional_sumsq, notional_min, notional_max, notional_first, notional_last)
    VALUES (1, (CAST(NEW.ts AS INTEGER) / 1 * 1), NEW.symbol, 1, NEW.ts, NEW.ts, coalesce((SELECT CASE WHEN NEW.price > 0 AND n >= 200 AND ((ln(NEW.price) - prev) - m) * ((ln(NEW.price) - prev) - m) > 36 * var THEN 1 ELSE 0 END FROM trades_base WHERE symbol = NEW.symbol), 0), NEW.price, NEW.price * NEW.price, NEW.price, NEW.price, NEW.price, NEW.price, NEW.size, NEW.size * NEW.size, NEW.size, NEW.size, NEW.size, NEW.size, ((NEW.price * NEW.size)), ((NEW.price * NEW.size)) * ((NEW.price * NEW.size)), ((NEW.price * NEW.size)), ((NEW.price * NEW.size)), ((NEW.price * NEW.size)), ((NEW.price * NEW.size)))
    ON CONFLICT (res, w, symbol) DO UPDATE SET
      n = n + 1,
      price_sum = price_sum + excluded.price_sum,
      price_sumsq = price_sumsq + excluded.price_sumsq,
      price_min = min(price_min, excluded.price_min),
      price_max = max(price_max, excluded.price_max),
      price_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.price_first ELSE price_first END,
      price_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.price_last ELSE price_last END,
      size_sum = size_sum + excluded.size_sum,
      size_sumsq = size_sumsq + excluded.size_sumsq,
      size_min = min(size_min, excluded.size_min),
      size_max = max(size_max, excluded.size_max),
      size_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.size_first ELSE size_first END,
      size_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.size_last ELSE size_last END,
      notional_sum = notional_sum + excluded.notional_sum,
      notional_sumsq = notional_sumsq + excluded.notional_sumsq,
      notional_min = min(notional_min, excluded.notional_min),
      notional_max = max(notional_max, excluded.notional_max),
      notional_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.notional_first ELSE notional_first END,
      notional_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.notional_last ELSE notional_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Rollup 1m.
  INSERT INTO trades_win (res, w, symbol, n, first_ts, last_ts, an, price_sum, price_sumsq, price_min, price_max, price_first, price_last, size_sum, size_sumsq, size_min, size_max, size_first, size_last, notional_sum, notional_sumsq, notional_min, notional_max, notional_first, notional_last)
    VALUES (60, (CAST(NEW.ts AS INTEGER) / 60 * 60), NEW.symbol, 1, NEW.ts, NEW.ts, coalesce((SELECT CASE WHEN NEW.price > 0 AND n >= 200 AND ((ln(NEW.price) - prev) - m) * ((ln(NEW.price) - prev) - m) > 36 * var THEN 1 ELSE 0 END FROM trades_base WHERE symbol = NEW.symbol), 0), NEW.price, NEW.price * NEW.price, NEW.price, NEW.price, NEW.price, NEW.price, NEW.size, NEW.size * NEW.size, NEW.size, NEW.size, NEW.size, NEW.size, ((NEW.price * NEW.size)), ((NEW.price * NEW.size)) * ((NEW.price * NEW.size)), ((NEW.price * NEW.size)), ((NEW.price * NEW.size)), ((NEW.price * NEW.size)), ((NEW.price * NEW.size)))
    ON CONFLICT (res, w, symbol) DO UPDATE SET
      n = n + 1,
      price_sum = price_sum + excluded.price_sum,
      price_sumsq = price_sumsq + excluded.price_sumsq,
      price_min = min(price_min, excluded.price_min),
      price_max = max(price_max, excluded.price_max),
      price_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.price_first ELSE price_first END,
      price_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.price_last ELSE price_last END,
      size_sum = size_sum + excluded.size_sum,
      size_sumsq = size_sumsq + excluded.size_sumsq,
      size_min = min(size_min, excluded.size_min),
      size_max = max(size_max, excluded.size_max),
      size_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.size_first ELSE size_first END,
      size_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.size_last ELSE size_last END,
      notional_sum = notional_sum + excluded.notional_sum,
      notional_sumsq = notional_sumsq + excluded.notional_sumsq,
      notional_min = min(notional_min, excluded.notional_min),
      notional_max = max(notional_max, excluded.notional_max),
      notional_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.notional_first ELSE notional_first END,
      notional_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.notional_last ELSE notional_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Rollup 1h.
  INSERT INTO trades_win (res, w, symbol, n, first_ts, last_ts, an, price_sum, price_sumsq, price_min, price_max, price_first, price_last, size_sum, size_sumsq, size_min, size_max, size_first, size_last, notional_sum, notional_sumsq, notional_min, notional_max, notional_first, notional_last)
    VALUES (3600, (CAST(NEW.ts AS INTEGER) / 3600 * 3600), NEW.symbol, 1, NEW.ts, NEW.ts, coalesce((SELECT CASE WHEN NEW.price > 0 AND n >= 200 AND ((ln(NEW.price) - prev) - m) * ((ln(NEW.price) - prev) - m) > 36 * var THEN 1 ELSE 0 END FROM trades_base WHERE symbol = NEW.symbol), 0), NEW.price, NEW.price * NEW.price, NEW.price, NEW.price, NEW.price, NEW.price, NEW.size, NEW.size * NEW.size, NEW.size, NEW.size, NEW.size, NEW.size, ((NEW.price * NEW.size)), ((NEW.price * NEW.size)) * ((NEW.price * NEW.size)), ((NEW.price * NEW.size)), ((NEW.price * NEW.size)), ((NEW.price * NEW.size)), ((NEW.price * NEW.size)))
    ON CONFLICT (res, w, symbol) DO UPDATE SET
      n = n + 1,
      price_sum = price_sum + excluded.price_sum,
      price_sumsq = price_sumsq + excluded.price_sumsq,
      price_min = min(price_min, excluded.price_min),
      price_max = max(price_max, excluded.price_max),
      price_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.price_first ELSE price_first END,
      price_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.price_last ELSE price_last END,
      size_sum = size_sum + excluded.size_sum,
      size_sumsq = size_sumsq + excluded.size_sumsq,
      size_min = min(size_min, excluded.size_min),
      size_max = max(size_max, excluded.size_max),
      size_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.size_first ELSE size_first END,
      size_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.size_last ELSE size_last END,
      notional_sum = notional_sum + excluded.notional_sum,
      notional_sumsq = notional_sumsq + excluded.notional_sumsq,
      notional_min = min(notional_min, excluded.notional_min),
      notional_max = max(notional_max, excluded.notional_max),
      notional_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.notional_first ELSE notional_first END,
      notional_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.notional_last ELSE notional_last END,
      first_ts = min(first_ts, excluded.first_ts),
      last_ts = max(last_ts, excluded.last_ts),
      an = an + excluded.an;
  -- Precomputed answers by symbol.
  INSERT INTO _pc_trades_by_symbol (symbol, n, price_last, last_ts) VALUES (NEW.symbol, 1, NEW.price, NEW.ts)
    ON CONFLICT (symbol) DO UPDATE SET n = n + 1, price_last = CASE WHEN excluded.last_ts >= last_ts THEN excluded.price_last ELSE price_last END, last_ts = max(last_ts, excluded.last_ts);
  -- Precomputed answers by symbol per day.
  INSERT INTO _pc_trades_by_symbol_per_day (symbol, period, n, size_sum, notional_sum, price_min, price_max, price_first, first_ts) VALUES (NEW.symbol, strftime('%Y-%m-%d', NEW.ts, 'unixepoch'), 1, NEW.size, ((NEW.price * NEW.size)), NEW.price, NEW.price, NEW.price, NEW.ts)
    ON CONFLICT (symbol, period) DO UPDATE SET n = n + 1, size_sum = size_sum + excluded.size_sum, notional_sum = notional_sum + excluded.notional_sum, price_min = min(price_min, excluded.price_min), price_max = max(price_max, excluded.price_max), price_first = CASE WHEN excluded.first_ts < first_ts THEN excluded.price_first ELSE price_first END, first_ts = min(first_ts, excluded.first_ts);
  -- Precomputed answers.
  INSERT INTO _pc_trades (g, n) VALUES (1, 1)
    ON CONFLICT (g) DO UPDATE SET n = n + 1;
  -- The baseline of steps: a plain mean and variance while warming up, then an exponentially weighted one
  -- that each step moves by at most 1.5 standard deviations. Steps judged unusual above leave it unchanged.
  INSERT INTO trades_base (symbol, n, m, var, prev) SELECT NEW.symbol, 0, 0.0, 0.0, ln(NEW.price) WHERE NEW.price > 0
    ON CONFLICT (symbol) DO UPDATE SET
      n = CASE WHEN n >= 200 AND ((excluded.prev - prev) - m) * ((excluded.prev - prev) - m) > 36 * var THEN n ELSE n + 1 END,
      m = CASE WHEN n >= 200 AND ((excluded.prev - prev) - m) * ((excluded.prev - prev) - m) > 36 * var THEN m WHEN n < 200 THEN m + ((excluded.prev - prev) - m) / (n + 1) ELSE m + 0.002 * min(max((excluded.prev - prev) - m, -1.5 * sqrt(var)), 1.5 * sqrt(var)) END,
      var = CASE WHEN n >= 200 AND ((excluded.prev - prev) - m) * ((excluded.prev - prev) - m) > 36 * var THEN var WHEN n < 200 THEN (n * var + ((excluded.prev - prev) - m) * ((excluded.prev - prev) - (m + ((excluded.prev - prev) - m) / (n + 1)))) / (n + 1) ELSE (1 - 0.002) * (var + 0.002 * min(max((excluded.prev - prev) - m, -1.5 * sqrt(var)), 1.5 * sqrt(var)) * min(max((excluded.prev - prev) - m, -1.5 * sqrt(var)), 1.5 * sqrt(var)) / 0.77846521617447) END,
      prev = excluded.prev;
END;

-- What this file holds, for any tool that opens it.
CREATE TABLE IF NOT EXISTS _precomputing (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing (key, value) VALUES
  ('format', '1'),
  ('compiler', 'precomputing 0.2.0'),
  ('policy', '# Stock trades turned into candles: the policy behind Demo 2.
# A candle is a window summary: open, high, low and close are the first,
# maximum, minimum and last price, volume is size_sum, and VWAP is
# notional_sum / size_sum.

stream trades {
  key    symbol text
  value  price real
  value  size  integer
  derive notional = price * size

  raw     keep 5m                     # every trade stays whole for five minutes
  rollup  1s keep 1h                  # 1-second candles for an hour
  rollup  1m keep 90d                 # 1-minute candles for 90 days
  rollup  1h keep forever             # hourly candles for good

  # A price jump: a step from the previous trade far outside the usual steps.
  anomalies price log change z > 6 memory 500 warmup 200 keep 5 per 1m
}

# The quote board, kept ready.
precompute last_price   = last(trades.price) by symbol
precompute day_open     = first(trades.price) by symbol per day
precompute day_high     = max(trades.price) by symbol per day
precompute day_low      = min(trades.price) by symbol per day
precompute day_volume   = sum(trades.size) by symbol per day
precompute day_turnover = sum(trades.notional) by symbol per day
precompute day_trades   = count(trades) by symbol per day
precompute trades_total = count(trades)
'),
  ('distill', '-- Distill for trades.precompute: run every few minutes with :now bound to the time of the newest event.

-- Stream trades.
DELETE FROM trades_raw WHERE ts < :now - 300;
DELETE FROM trades_win WHERE res = 1 AND w <= :now - 3601;
DELETE FROM trades_win WHERE res = 60 AND w <= :now - 7776060;
');
CREATE TABLE IF NOT EXISTS _precomputing_objects (name TEXT PRIMARY KEY, kind TEXT NOT NULL, stream TEXT NOT NULL, detail TEXT NOT NULL);
INSERT OR REPLACE INTO _precomputing_objects (name, kind, stream, detail) VALUES
  ('trades', 'stream', 'trades', '{"derived":[{"name":"notional","sql":"(NEW.price * NEW.size)"}],"insert":"INSERT INTO trades (ts, symbol, price, size) VALUES (...)","keys":["symbol"],"raw_keep_seconds":300,"raw_table":"trades_raw","values":["price","size"]}'),
  ('trades_win', 'windows', 'trades', '{"numbers":["price","size","notional"],"rollups":[{"keep_seconds":3600,"quantiles":[],"res_seconds":1},{"keep_seconds":7776000,"quantiles":[],"res_seconds":60},{"keep_seconds":-1,"quantiles":[],"res_seconds":3600}]}'),
  ('trades_anomaly', 'anomalies', 'trades', '{"baseline_table":"trades_base","change":true,"keep":5,"log":true,"memory":500,"per_seconds":60,"value":"price","warmup":200,"z":6}'),
  ('last_price', 'precompute', 'trades', '{"by":["symbol"],"function":"last","per":null,"state":"_pc_trades_by_symbol","value":"price"}'),
  ('day_open', 'precompute', 'trades', '{"by":["symbol"],"function":"first","per":"day","state":"_pc_trades_by_symbol_per_day","value":"price"}'),
  ('day_high', 'precompute', 'trades', '{"by":["symbol"],"function":"max","per":"day","state":"_pc_trades_by_symbol_per_day","value":"price"}'),
  ('day_low', 'precompute', 'trades', '{"by":["symbol"],"function":"min","per":"day","state":"_pc_trades_by_symbol_per_day","value":"price"}'),
  ('day_volume', 'precompute', 'trades', '{"by":["symbol"],"function":"sum","per":"day","state":"_pc_trades_by_symbol_per_day","value":"size"}'),
  ('day_turnover', 'precompute', 'trades', '{"by":["symbol"],"function":"sum","per":"day","state":"_pc_trades_by_symbol_per_day","value":"notional"}'),
  ('day_trades', 'precompute', 'trades', '{"by":["symbol"],"function":"count","per":"day","state":"_pc_trades_by_symbol_per_day","value":null}'),
  ('trades_total', 'precompute', 'trades', '{"by":[],"function":"count","per":null,"state":"_pc_trades","value":null}');
