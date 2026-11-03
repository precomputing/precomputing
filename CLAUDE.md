# Notes for Claude

This file says where the next session starts and what comes next. What comes
next is decided with the owner before it goes here, and work starts only when
the owner says go.

## Where the next session starts

Precomputing 0.3 (softdev-plan item 28, photozeitgeist/jollyroger.biz) is in
progress; the steps below say what is done.

## Releases

Releases are automated. To release, raise `Version` in
`internal/version/version.go`, add a CHANGELOG section and push to `main`;
never ask the owner to publish one. [RELEASING.md](RELEASING.md) has the
whole procedure and the checks to run afterwards.

## Project facts

Precomputing keeps answers to event streams (counts, sums, percentiles,
rollups, samples, anomalies) current in a SQLite file, from a small policy
language. Two runtimes write the same file value for value: the compiled SQL
triggers (`compile/`) and the Go Engine (`engine/`), plus the Meter (exact
streams), Logs (`logs/`), Traces (`traces/`) and an MCP server (`mcp/`).
Code: precomputing/precomputing (module precomputing.com/precomputing),
Apache 2.0, standard library only, SQLite amalgamation through cgo in
`internal/sqlite`. Site: photozeitgeist/precomputing.com (Hugo, standard
template, content only). Domain: precomputing.com.

Rules worth knowing before changing anything:
- Every change to how events are counted goes into both runtimes, and the
  equivalence tests (`engine/sqlitestore/equiv_test.go`) must still show the
  two files equal value for value.
- The file format changes only by adding (`docs/file-format.md`).
- Docs end with the limits of the release; README is the manual, docs/ is
  reference.

## Working here

- `go test ./...`, `go vet ./...`, race with `-race -short`. The suite takes
  about two minutes and needs a C compiler.
- This cloud session has Go 1.24 only. From 0.3 go.mod says 1.26, so local
  runs use a scratch go.mod: `go test -modfile=$SCRATCH/go.mod ./...` with
  `go 1.24` in it. CI on current stable Go is the real check.
- Golden SQL files record the compiler version; they change with `Version`.

## In progress: Precomputing 0.3 (softdev-plan item 28, approved by YK 2026-11-03)

What and why: one future-stamped event can close a billing month early, since
an exact stream's clock is its newest event; `serve` has tokens but no TLS, so
it needs a proxy outside localhost; teams already run OpenTelemetry and need a
custom sender to reach it; and a policy is fixed for the life of a file, the
biggest obstacle to running one for months.

Design decisions:
- Phase A1, clock guard: an exact stream may say `ahead 10m`. An event stamped
  more than that past the wall clock, at the moment it is counted, is refused
  as `ahead` and counted in `NAME_refused`, before the late, closed and repeat
  checks, so it never moves the stream's clock. Triggers compare with
  `CAST(strftime('%s','now') AS INTEGER)`; the Engine with the system clock
  (injectable for tests). This is the one rule that reads the wall clock;
  events from the past, which is every replay, are never affected by it.
  Leaving the line out keeps 0.2's behaviour.
- Phase A2, TLS: `serve --tls-cert FILE --tls-key FILE`, TLS 1.2 or later; the
  pair is read again when either file changes, so a renewed certificate needs
  no restart. Agents are told `https://`. Without TLS outside localhost the
  server warns, as it already does for tokens.
- Phase B, OTLP/HTTP intake in `serve`: `POST /v1/logs` and `POST /v1/metrics`,
  protobuf (`application/x-protobuf`) or JSON (`application/json`), gzip
  accepted, decoded by hand (no dependencies), answered in the request's
  encoding, write token needed. Log records go to the streams from logs:
  fields are the record's, scope's and resource's attributes with dots made
  underscores (`http.route` is `http_route`), `service` from `service.name`,
  `level` from the severity text, the message from the body; the template is
  learned from the message as for lines. Metrics need a new stream form,
  `stream NAME from metrics "metric.name" [where F = "V" ...]`: keys are
  attributes named the same way; a gauge or sum point fills the stream's one
  value, a histogram point fills values named `count`, `sum`, `min` and `max`.
  Cumulative sums and histograms become deltas per series; a restart of the
  series (new start time) counts from zero. Points of other kinds are skipped
  and counted. OTLP has no sequence numbers, so a retried export after a
  crash may count twice; the docs say so.
- Phase C, policy migration: `precomputing migrate --policy NEW FILE`
  (offline, one writer). Allowed: new streams, rollups, precomputes, quotas,
  samples and anomaly settings, keep durations, `raw until closed + D`, and
  `ahead`. Refused, with the reason: removing or renaming anything, changing
  keys, values, ids, types, `period`, `close`, `late`, quantile accuracy, or
  a table's columns (such as a precompute that would add a column to state
  shared with an existing one), and quantiles added to an existing rollup.
  New objects start empty at the migration; `_precomputing_migrations`
  records when and what started, and `inspect` shows it. The migration runs
  in one transaction: triggers and views are made again, new tables added,
  `_precomputing` and `_precomputing_objects` rewritten. Format stays 1.
- Upkeep: go.mod 1.26, workflows on stable Go, README needs Go 1.26 and its
  Status describes 0.3.0.
- Version 0.3.0.

Steps:
1. [x] Phase 0 and upkeep (this file, go.mod 1.26, workflows on stable Go, README status) (a611b7a).
2. [x] A1 clock guard (940f4cb): TestAheadMatchesSQL (3 planted reports refused as ahead in both runtimes, files equal, no clock moved, the same events without the rule close September early), TestAheadWallClock, parser tests. Distill's :now is the newest counted event (the test feeder had bound it to a refused time).
3. [x] A2 TLS (200ae8b): cmd/precomputing/tls.go, files compared by content at each handshake; TestTLS (renewal, half-written pair keeps the last good one, TLS 1.1 and plain HTTP refused); checked with curl and an openssl certificate.
4. [x] B OTLP (1819256): otlp/ (decode.go, json.go, intake.go), fixtures from the official opentelemetry-proto classes (otlp/testdata/generate.py), both encodings decode equal; intake tests for logs, cumulative baseline, delta, restart, a series newer than the intake, rejections, request digests; serve endpoints with write tokens, gzip, 415/400/413, partial success; checked with curl. Added beyond the plan: request digests for an hour so a retry counts once, and series state in the file.
5. [x] C migrate (92ab2b2): migrate/ (schemas compared in scratch databases and against the file, policy-level rules in compare.go), CLI with --dry-run, inspect lists migrations; tests: both runtimes equal after a migration and old tables equal to a never-migrated file, 15 refusals, failure part way at four points leaves the file unchanged, a name in use.
6. [x] Docs: README, language (ahead, streams from metrics, OTLP records, Changing a Policy), engine (TLS, OpenTelemetry), meter, logs, file-format, CHANGELOG; examples/otel.precompute with golden SQL.
7. [ ] Independent review (at most two rounds), fixes, fresh results.
8. [ ] 0.3.0 released by the workflow, downloads checked.
9. [ ] precomputing.com post with its own SVG, pages; softdevstudio.com note;
   softdev-plan log row.
