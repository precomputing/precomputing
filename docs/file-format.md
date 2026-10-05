# The File Format

A Precomputing file is an ordinary SQLite file. Any SQLite tool opens it and the same SQL works on it, whichever runtime wrote it. This page describes format 1, the layout written by versions 0.1 and 0.1.1; 0.1.1 only adds the tables of the store of agent calls. The file records its own format and policy, so a reader never has to guess.

## What Is in the File

For a stream named `latency` with key `endpoint` and value `ms`:

| Name | Kind | Holds |
|---|---|---|
| `latency` | view | Where events are inserted. Reading it shows the raw events still kept |
| `latency_raw` | table | Whole events, for as long as `raw keep` says |
| `latency_win` | table | One summary per rollup resolution, window and key |
| `latency_ms_sk` | table | Quantile sketch buckets of `ms`, per rollup resolution, window and key |
| `latency_sample` | table | A few whole events per window |
| `latency_anomaly` | table | Unusual events kept whole, with their z-score |
| `latency_base` | table | The running baseline anomalies are judged against |
| `latency_ingest` | trigger | Does the work on every insert |
| `requests`, `avg_ms`, `p99_ms` | views | The precomputed answers, one per precompute, named as in the policy |
| `_pc_latency_by_endpoint` | table | State behind count, sum, avg, min, max, first and last answers |
| `_pcq_latency_ms_by_endpoint` | table | Sketch behind percentile answers |
| `_precomputing` | table | `format`, `compiler`, the full `policy` text and the `distill` statements |
| `_precomputing_objects` | table | Every object above with its kind, stream and settings as JSON |
| `_precomputing_sources` | table | Written by the Engine only: for each sender, the last sequence number the file holds |

## Window Summaries

`latency_win` has the key columns `res` (window length in seconds), `w` (window start, seconds since 1970, a multiple of `res`) and the stream's keys. Then:

| Column | Meaning |
|---|---|
| `n` | Events in the window |
| `first_ts`, `last_ts` | Time of the earliest and latest event |
| `an` | Events judged unusual, including those not kept whole |
| `ms_sum`, `ms_sumsq` | Sum and sum of squares of `ms` |
| `ms_min`, `ms_max` | Smallest and largest `ms` |
| `ms_first`, `ms_last` | `ms` of the earliest and latest event; ties go to the one inserted first and last |

Mean is `ms_sum / n`. Variance is `ms_sumsq / n - (ms_sum / n) * (ms_sum / n)`. Summaries merge by adding `n`, sums and `an`, taking the minimum of minimums and the maximum of maximums, and keeping first and last by time. That rule is what lets a runtime fill one window from several sources.

## Sketches

A sketch bucket `b` counts the values `v` with `ceil(ln(v) / ln(γ)) = b`, where `γ = (1 + α) / (1 − α)` and `α` is the stream's quantile accuracy (0.01 unless the policy says otherwise; `γ` is stored in `_precomputing_objects`). Zero and negative values go to bucket `-1000000`. The estimate for a bucket is `2 * γ^b / (γ + 1)`, which is within `α` of every value in it. Sketches merge by adding `n` per bucket.

The p99 of `/api/checkout` for one hour from the 1-minute sketches:

```sql
WITH b AS (
  SELECT b, sum(n) AS n FROM latency_ms_sk
  WHERE res = 60 AND endpoint = '/api/checkout' AND w >= 1790589600 AND w < 1790593200
  GROUP BY b),
c AS (SELECT b, sum(n) OVER (ORDER BY b) AS cum, sum(n) OVER () AS tot FROM b)
SELECT 2 * pow(1.02020202020202, min(b)) / (1.02020202020202 + 1) AS p99
FROM c WHERE cum >= 0.99 * tot;
```

## Samples and Anomalies

`latency_sample` keeps up to N events per window in slots `0` to `N-1`, filled first and then replaced with falling probability, so each event of the window has the same chance of being kept. The choice uses a fixed pseudo-random sequence, so the same events always give the same samples.

`latency_base` holds, per key, `n` (events that updated it), the mean `m` and the variance `var` of the value (or of its logarithm, with `log`). After warmup the variance is updated from deviations capped at 1.5 standard deviations and divided by 0.7785, the share of the variance such a cap keeps for normal data, so it stays an unbiased estimate. `latency_anomaly` holds the unusual events with `z`, their distance from the baseline in standard deviations at the moment they arrived.

With `change`, the baseline table has one more column, `prev`: the key's previous value (or its logarithm). The baseline then describes the steps between events, `n` counts steps, and `z` is the step's distance from the usual steps.

## Exact Streams and Quotas

An exact stream named `usage`, with identifier `request_id` and keys `customer` and `model`, adds three tables. A quota adds a table and a view.

| Name | Kind | Holds |
|---|---|---|
| `usage_ids` | table | `request_id` and `ts` of every event counted within the repeat window, to refuse repeats |
| `usage_clock` | table | One row, `g = 1`, with `newest`: the newest event time counted, which the late and closed rules go by |
| `usage_refused` | table | `reason` (`late`, `closed` or `repeat`), the stream's keys and `n`, the events refused |
| `monthly_tokens_limit` | table | A quota's limits: the precompute's `by` keys and `lim` |
| `monthly_tokens` | view | For each key with a limit and each period: `used`, `lim`, `remaining` and `reached` |

The raw table of an exact stream also holds the identifier. A refused event touches `usage_refused` and nothing else. An accepted one is written to `usage_ids`, `usage_clock` and the raw table, then summarised as in any stream.

## Streams from Logs

A stream from logs has one more column, `line TEXT`, in its raw table, its samples and its anomalies: the whole log line. A policy with a `logs` block adds one table:

| Name | Kind | Holds |
|---|---|---|
| `_precomputing_templates` | table | Every template learned: `id` (the number streams use as their `template` key), `service`, `level`, `template` (with `<*>` where lines differ), `initial` (the tokens it started from, which place it in the template tree), `n` (lines), `first_ts`, `last_ts` and `example` (its first line) |

The Engine writes the templates at each checkpoint, in the same transaction as the rows. How lines are read is stored as JSON in `_precomputing_objects`, under the name `logs`.

## Agent Calls from Traces

`precomputing traces` adds two tables and a view to the file, beside the policy's own:

| Name | Kind | Holds |
|---|---|---|
| `trace_pieces` | table | Each message or tool list once: `id` (numbered in the order first seen), `sha256` (of `body`, unique), `source` (`system`, `tools`, `task`, `user`, `assistant` or `tool:NAME`), `tokens`, `bytes` and `body`, the piece as sent with secrets masked |
| `trace_calls` | table | Each call once, under `call_id` (the run and the call's number, such as `run-17#4`): `run`, `seq`, `ts` (when the reply came back), `repo`, `model`, `messages` (a JSON array of piece ids, in order), `tools` and `reply` (piece ids), `request_bytes`, `request_sha256` (of the request as sent, with secrets masked), `input_tokens`, `cached_tokens` and `output_tokens`, with an index on `run, seq` |
| `trace_requests` | view | Each call rebuilt from its pieces with SQL alone: `call_id`, `run`, `seq`, `request` (the request body as sent, byte for byte, with secrets masked) and `reply` |

The Engine writes new pieces and calls at each checkpoint, in the same transaction as the rows. A request rebuilt by the view has the SHA-256 kept in `request_sha256`. Only the store writes these tables; any SQLite tool reads them, and the view needs SQLite 3.44 or later, for `group_concat` with `ORDER BY`. The MCP server adds nothing to a file: it only reads.

## The Engine's Senders

The Engine adds one table, `_precomputing_sources`, with one row per sender: `source`, `seq` (the last sequence number the file holds from it), `events` (events the file holds from it) and `newest` (the newest event time from it). A checkpoint writes it in the same transaction as the rows, so after a crash `seq` says exactly where to resend from. Files written by the compiled SQL alone do not have it, and nothing else in the file depends on it.

## Retention

The distill statements, stored under `_precomputing.distill`, delete raw events older than `raw keep` and windows (with their sketch buckets and samples) whose end is older than their rollup's `keep`. For an exact stream they delete identifiers older than the repeat window, and the raw events of every period that closed at least `D` ago, for `raw until closed + D`. They take one parameter, `:now`, the time of the newest event. Anomalies, baselines, precomputed answers and refusal counts are never deleted.

## Either Runtime

The Engine writes the tables above exactly as the compiled triggers would, value for value, and the triggers are in its file too. So the SQL runtime can carry on in a file the Engine wrote, with plain `INSERT` statements, and the Engine can open a file the triggers filled and carry on from it. One file should have one writer at a time.

## Compatibility

Format 1 changes only by adding. A future release may add tables and columns; it will not change the meaning of the ones above. A file records the format it was written in, under `_precomputing.format`.
