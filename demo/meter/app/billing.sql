-- Billing for Demo 3: the product's own tables and views, in the same file as the meter.
-- Money is kept in billionths of a dollar (nano-dollars) as integers, so every sum is exact.

CREATE TABLE IF NOT EXISTS plans (plan TEXT PRIMARY KEY, name TEXT NOT NULL, quota INTEGER, discount_pct INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS customers (customer TEXT PRIMARY KEY, name TEXT NOT NULL, plan TEXT NOT NULL REFERENCES plans, gateway TEXT NOT NULL) WITHOUT ROWID;
-- List prices and what the product pays for each model, per token.
CREATE TABLE IF NOT EXISTS prices (model TEXT PRIMARY KEY, input_nano INTEGER NOT NULL, output_nano INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS model_costs (model TEXT PRIMARY KEY, input_nano INTEGER NOT NULL, output_nano INTEGER NOT NULL) WITHOUT ROWID;

-- One line per customer, model and month, read from the meter's precomputes.
CREATE VIEW IF NOT EXISTS invoice_lines AS
SELECT r.period, r.customer, r.model, r.value AS requests,
  CAST(i.value AS INTEGER) AS input_tokens, CAST(o.value AS INTEGER) AS output_tokens,
  CAST(i.value AS INTEGER) * p.input_nano + CAST(o.value AS INTEGER) * p.output_nano AS list_nano,
  CAST(i.value AS INTEGER) * k.input_nano + CAST(o.value AS INTEGER) * k.output_nano AS cost_nano
FROM requests_month r
JOIN input_month i USING (customer, model, period)
JOIN output_month o USING (customer, model, period)
JOIN prices p USING (model)
JOIN model_costs k USING (model);

-- One invoice per customer and month. The discount is rounded down to the nano-dollar.
CREATE VIEW IF NOT EXISTS invoices AS
SELECT l.period, l.customer, c.name, c.plan, sum(l.requests) AS requests,
  sum(l.input_tokens) AS input_tokens, sum(l.output_tokens) AS output_tokens,
  sum(l.list_nano) AS list_nano,
  sum(l.list_nano) * pl.discount_pct / 100 AS discount_nano,
  sum(l.list_nano) - sum(l.list_nano) * pl.discount_pct / 100 AS due_nano,
  (sum(l.list_nano) - sum(l.list_nano) * pl.discount_pct / 100 + 5000000) / 10000000 AS due_cents,
  sum(l.cost_nano) AS cost_nano
FROM invoice_lines l
JOIN customers c USING (customer)
JOIN plans pl USING (plan)
GROUP BY l.period, l.customer;
