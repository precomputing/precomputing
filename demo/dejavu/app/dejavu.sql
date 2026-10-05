-- Incident deja vu: the tables, views and trigger Demo 6 adds to the file, beside the policy's own.
--
-- incidents holds what the on-call team wrote down about each incident: when it started, what it
-- was and what fixed it. A fingerprint is the set of log templates whose rate is far from usual:
-- at least three times higher or lower, and at least six lines a minute apart. Usual is the median
-- minute of the hour before, so an earlier incident in that hour doesn't move it. A template rising
-- is judged on the last minute or two; one going quiet must be quiet over one minute more as well,
-- since quiet takes longer to be sure of. Each template is scored with the logarithm of the ratio,
-- so one that appears from nothing scores high and one that goes quiet scores below zero. DEBUG
-- lines are left out.
--
-- Writing an incident down saves its fingerprint beside it, taken from the minute windows of its
-- first two minutes, with the newest raw line of each template while the raw lines are still kept.
-- The saved fingerprint outlives the seven days the minute windows are kept.
--
-- deja_vu compares the fingerprint of the last minute or two with every saved one, by the cosine
-- of the two sets of scores, and lists the incidents from the closest down.

CREATE TABLE IF NOT EXISTS incidents (
  id      INTEGER PRIMARY KEY,
  started INTEGER NOT NULL,        -- seconds since 1970, UTC
  ended   INTEGER,
  title   TEXT NOT NULL,
  fix     TEXT NOT NULL,
  source  TEXT NOT NULL            -- who wrote it down
);

CREATE TABLE IF NOT EXISTS incident_keys (
  incident   INTEGER NOT NULL REFERENCES incidents (id),
  service    TEXT NOT NULL,
  level      TEXT NOT NULL,
  template   INTEGER NOT NULL,
  now_rate   REAL NOT NULL,        -- lines a minute in the incident's first two minutes
  usual_rate REAL NOT NULL,        -- lines in the median minute of the hour before
  score      REAL NOT NULL,
  example    TEXT,                 -- the newest raw line of the template when it was written down
  PRIMARY KEY (incident, service, level, template)
);

-- The newest line in the file sets the time.
CREATE VIEW IF NOT EXISTS dejavu_clock AS
SELECT max(newest) AS now FROM _precomputing_sources;

-- The last minute or two (the minute before this one, and this one so far) against the hour before.
CREATE VIEW IF NOT EXISTS fingerprint_now AS
WITH t AS (SELECT now, now / 60 * 60 - 60 AS since FROM dejavu_clock),
cur AS (
  SELECT service, level, template,
    sum(CASE WHEN w >= t.since THEN n ELSE 0 END) * 60.0 / (t.now + 1 - t.since) AS fast,
    sum(n) * 60.0 / (t.now + 61 - t.since) AS slow     -- one minute more
  FROM lines_win, t
  WHERE res = 60 AND w >= t.since - 60 AND level <> 'DEBUG'
  GROUP BY service, level, template),
hour AS (
  SELECT service, level, template, n,
    row_number() OVER (PARTITION BY service, level, template ORDER BY n DESC) AS rk
  FROM lines_win, t
  WHERE res = 60 AND w >= t.since - 3600 AND w < t.since AND level <> 'DEBUG'),
base AS (   -- the median of 60 minutes, a minute without lines counting as zero
  SELECT service, level, template, sum(CASE WHEN rk IN (30, 31) THEN n ELSE 0 END) / 2.0 AS rate
  FROM hour GROUP BY service, level, template),
k AS (SELECT service, level, template FROM cur UNION SELECT service, level, template FROM base),
r AS (
  SELECT k.service, k.level, k.template, coalesce(cur.fast, 0) AS fast, coalesce(cur.slow, 0) AS slow,
    coalesce(base.rate, 0) AS usual_rate
  FROM k LEFT JOIN cur USING (service, level, template) LEFT JOIN base USING (service, level, template)),
s AS (   -- rising: the last minute or two; going quiet: that and one minute more
  SELECT service, level, template, usual_rate,
    CASE WHEN fast >= usual_rate THEN fast ELSE min(max(fast, slow), usual_rate) END AS now_rate
  FROM r)
SELECT service, level, template, now_rate, usual_rate, ln((now_rate + 2) / (usual_rate + 2)) AS score
FROM s
WHERE abs(ln((now_rate + 2) / (usual_rate + 2))) >= ln(3) AND abs(now_rate - usual_rate) >= 6;

-- The first two minutes of every incident written down, against the hour before, from the minute
-- windows while they are kept.
CREATE VIEW IF NOT EXISTS incident_fingerprint AS
WITH i AS (SELECT id, started / 60 * 60 AS since FROM incidents),
cur AS (
  SELECT i.id AS incident, service, level, template, sum(n) / 2.0 AS rate
  FROM i JOIN lines_win l ON l.res = 60 AND l.w >= i.since AND l.w < i.since + 120
  WHERE level <> 'DEBUG'
  GROUP BY i.id, service, level, template),
hour AS (
  SELECT i.id AS incident, service, level, template, n,
    row_number() OVER (PARTITION BY i.id, service, level, template ORDER BY n DESC) AS rk
  FROM i JOIN lines_win l ON l.res = 60 AND l.w >= i.since - 3600 AND l.w < i.since
  WHERE level <> 'DEBUG'),
base AS (
  SELECT incident, service, level, template, sum(CASE WHEN rk IN (30, 31) THEN n ELSE 0 END) / 2.0 AS rate
  FROM hour GROUP BY incident, service, level, template),
k AS (SELECT incident, service, level, template FROM cur UNION SELECT incident, service, level, template FROM base),
r AS (
  SELECT k.incident, k.service, k.level, k.template, coalesce(cur.rate, 0) AS now_rate, coalesce(base.rate, 0) AS usual_rate
  FROM k LEFT JOIN cur USING (incident, service, level, template) LEFT JOIN base USING (incident, service, level, template))
SELECT incident, service, level, template, now_rate, usual_rate, ln((now_rate + 2) / (usual_rate + 2)) AS score
FROM r
WHERE abs(ln((now_rate + 2) / (usual_rate + 2))) >= ln(3) AND abs(now_rate - usual_rate) >= 6;

-- Writing an incident down saves its fingerprint and an example line of each template in it.
CREATE TRIGGER IF NOT EXISTS incidents_fingerprint AFTER INSERT ON incidents
BEGIN
  INSERT INTO incident_keys (incident, service, level, template, now_rate, usual_rate, score, example)
  SELECT f.incident, f.service, f.level, f.template, f.now_rate, f.usual_rate, f.score,
    (SELECT r.line FROM lines_raw r
     WHERE r.service = f.service AND r.level = f.level AND r.template = f.template
       AND r.ts >= NEW.started / 60 * 60 AND r.ts < coalesce(NEW.ended, NEW.started + 3600)
     ORDER BY r.ts DESC LIMIT 1)
  FROM incident_fingerprint f WHERE f.incident = NEW.id;
END;

-- Every incident written down, closest to the last minute or two first. Empty while nothing stands out.
CREATE VIEW IF NOT EXISTS deja_vu AS
WITH n AS MATERIALIZED (SELECT service, level, template, score FROM fingerprint_now),
nn AS (SELECT sqrt(sum(score * score)) AS norm FROM n),
fn AS (SELECT incident, sqrt(sum(score * score)) AS norm, count(*) AS keys FROM incident_keys GROUP BY incident),
d AS (
  SELECT k.incident, sum(n.score * k.score) AS dot, count(*) AS shared
  FROM n JOIN incident_keys k USING (service, level, template) GROUP BY k.incident)
SELECT i.id, i.title, i.fix, i.started, i.source, coalesce(d.dot, 0) / (nn.norm * fn.norm) AS similarity,
  coalesce(d.shared, 0) AS shared, fn.keys AS keys
FROM incidents i JOIN fn ON fn.incident = i.id JOIN nn LEFT JOIN d ON d.incident = i.id
WHERE nn.norm > 0
ORDER BY similarity DESC, i.started DESC;
