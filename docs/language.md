# The Policy Language

A policy is a short text file, `name.precompute`. It names the streams of events you have and the answers to keep ready, and says how long each level of detail survives. The compiler turns it into plain SQLite. After that you insert events like rows in a table, and the answers keep themselves current.

```text
stream latency {
  key   endpoint text
  value ms real

  raw     keep 5m
  rollup  10s keep 24h
  rollup  1m  keep 30d  quantiles ms
  rollup  1h  keep 1y   quantiles ms

  samples 3 per 1m
  anomalies ms log z > 4 keep 20 per 1m
}

precompute requests = count(latency) by endpoint
precompute avg_ms   = avg(latency.ms) by endpoint
precompute p99_ms   = p99(latency.ms) by endpoint
```

```sh
precomputing compile latency.precompute | sqlite3 latency.db
sqlite3 latency.db "INSERT INTO latency (ts, endpoint, ms) VALUES (1790586000, '/api/search', 31.5)"
sqlite3 latency.db "SELECT * FROM p99_ms"
```

## Streams

A stream is a sequence of events that share the same fields. Every event has a time, `ts`, in seconds since 1970 (UTC). Insert events into the stream's name as if it were a table: `INSERT INTO latency (ts, endpoint, ms) VALUES (?, ?, ?)`. None of the fields may be null.

| Line | What it does |
|---|---|
| `key endpoint text` | A field events are grouped by. `text` (the default) or `integer` |
| `value ms real` | A measured number. `real` (the default) or `integer` |
| `derive notional = price * size` | A number computed from other values of the same event, with `+ - * /` and parentheses. It is summarised like any value |
| `raw keep 10m` | Keep whole events for this long. Leave the line out to keep none |
| `rollup 1m keep 30d` | Keep one summary per key per window of this length, for this long. `keep forever` never deletes |
| `rollup 1m keep 30d quantiles ms` | The same, plus a quantile sketch of `ms` in every window, so percentiles such as p99 survive the rollup |
| `samples 3 per 1m` | Keep three whole events from every 1-minute window, chosen evenly from all of them. Needs a `rollup 1m` |
| `anomalies ms log z > 4 keep 20 per 1m` | Keep unusual events whole. See below |
| `quantile accuracy 1%` | How close sketch percentiles are to the exact values. 1% is the default |

Every window summary holds, for each value: the count, the sum, the sum of squares, the minimum, the maximum, the first and the last. From these come the mean, the variance and the standard deviation, and for prices the open, high, low and close. All of them merge: two 30-second summaries add up to exactly the 1-minute summary, which is why coarse rollups lose no accuracy for these numbers.

Percentiles do not merge from those numbers, so a rollup that should answer p99 needs `quantiles`. The sketch keeps counts in buckets about 2% wide, which merge by adding, and every percentile read from it is within the stated accuracy of the exact value.

### Anomalies

```text
anomalies ms log z > 4 memory 1000 warmup 500 keep 20 per 1m
```

Each event is compared with a running baseline of its own key. The baseline is a mean and a variance, plain for the first `warmup` events and then weighted towards about the last `memory` events. An event more than `z` standard deviations from the baseline is kept whole in the `latency_anomaly` table, with its z-score. `log` judges the logarithm of the value, which suits latencies, prices and sizes that spread by ratios.

Unusual events do not move the baseline, and any other event moves it by at most 1.5 standard deviations (the variance is corrected for that cap). An outage therefore stays unusual for as long as it lasts, while slow drifts are still learned. `keep 20 per 1m` caps what is kept during such a storm. After 20 in one minute for one key, further anomalies are still counted in the window summary (`an`), and they are no longer kept whole. The defaults are memory 3000, warmup 500 and keep 20 per the first rollup of a minute or longer.

```text
anomalies price log change z > 6 memory 500 warmup 200 keep 5 per 1m
```

`change` judges each event by its step from the previous event of the same key. That is the right test for prices. A price that jumps 8% in one trade is one unusual step, and the trades after it are judged from the new price. Judged by level, every trade at the new price would look unusual for good. With `log`, the step is the change in the logarithm, which is the trade's return. The first event of a key only records its value, and the baseline learns the usual steps.

## Precomputes

```text
precompute NAME = FUNCTION(STREAM.VALUE) [by KEY, ...] [per hour|day|month]
```

| Function | Answer |
|---|---|
| `count(stream)` | Number of events |
| `sum`, `avg`, `min`, `max` | Over all events |
| `first`, `last` | The value of the earliest or latest event by `ts` |
| `p50`, `p90`, `p95`, `p99`, `p999`, any `pNN` | Percentile from a sketch, within the stream's quantile accuracy |

`by` groups the answer by keys of the stream. `per` adds a `period` column (`2026-09`, `2026-09-28` or `2026-09-28T09`, in UTC), so a month's total starts at zero on the first. Each precompute becomes a view with the same name: `SELECT * FROM p99_ms` returns one row per endpoint with a `value` column. Precomputes that share a stream, `by` and `per` share their stored state, so adding one costs little.

## Exact Streams

Some numbers have to be right to the unit, such as tokens billed or calls made. An exact stream is for them.

```text
stream usage exact {
  id     request_id refuse repeats 7d
  key    customer text
  key    model text
  value  input_tokens integer
  value  output_tokens integer
  derive tokens = input_tokens + output_tokens

  late   48h
  period month close 24h
  raw    until closed + 90d
  rollup 1h keep 400d
}
```

| Line | What it does |
|---|---|
| `id request_id refuse repeats 7d` | Every event carries an identifier, `text` (the default) or `integer`. An event whose identifier was counted in the last seven days is refused, so a retried request counts once |
| `late 48h` | An event more than 48 hours older than the newest event counted so far is refused. Leave the line out to accept events of any age while their period is open |
| `period month close 24h` | Periods are hours, days or months, in UTC. A period closes 24 hours after it ends, and from then on an event that falls in it is refused. Required |
| `raw until closed + 90d` | Every event is kept whole until its period has been closed for 90 days, so any total can be checked event by event while it can still be disputed. Required |

The rules are checked in that order: late, closed, repeat. A refused event changes one thing only, a counter in `usage_refused` by reason and by the stream's keys, so whatever was not counted is known exactly. "Newest" and "closed" go by the times on the events and never by the clock on the wall, so sending the same events again always gives the same file.

Every summary and precompute of an exact stream is exact. Integer values are added exactly, and a sum stays exact up to 2^53, about nine million billion. Quantile sketches are approximate, so an exact stream allows neither `quantiles` nor percentile precomputes.

## Quotas

```text
precompute tokens_month = sum(usage.tokens) by customer per month
quota monthly_tokens = tokens_month
```

A quota puts a limit next to a precompute. The compiler adds a table for the limits, `monthly_tokens_limit`, with the precompute's `by` keys and `lim`, and a view, `monthly_tokens`, with `used`, `lim`, `remaining` and `reached` for every key and period that has a limit. A key without a row in the limit table has no limit. A quota limits a `sum` or a `count` that has a `per`, so it starts again every period.

Events over the limit are still counted, because they happened. A gateway or an application reads the view before it serves the next request and turns the request away when `reached` is 1. Reading it is one lookup by primary key.

## Streams from Logs

A stream can take its events from log lines instead of from inserts. The `logs` block says how lines are read, and each stream from logs says which lines it takes.

```text
logs {
  format "<timestamp> <level> <service> <message>"
}

stream web from logs where service = "web" {
  key    route text
  value  ms real
  rollup 1m keep 30d quantiles ms
}
```

| Line | What it does |
|---|---|
| `format "..."` | The fields of a line in order. The text between fields is a regular expression; a run of spaces matches any run of white space. It needs `<message>`, and `<timestamp>` or `<date>` and `<time>` |
| `mask "REGEX"` | Masks more of the message before its template is learned. Values of `key=value` pairs are always masked |
| `templates similarity 0.5 depth 4` | How close lines must be to share a template, and the depth of the template tree. These are the defaults |
| `stream NAME from logs where F = "V" and ...` | The stream takes the lines whose fields equal those texts and that have every key and value it declares |

A line's fields are the format's fields, each `key=value` pair in the message, and `template`, the number of its template, declared as `key template integer`. The line itself is kept with raw events, samples and anomalies. A stream from logs may have no values, only counting lines. The Engine reads the lines (`precomputing put --lines`); the compiled SQL takes the fields and the line in an insert, with the template number given by the caller. [Logs](logs.md) has the rest.

## Letting Detail Fade

Detail is removed by the distill statements, which the compiler writes separately (`precomputing compile --distill`) and also stores inside the file. Run them every few minutes with `:now` bound to the time of the newest event. They delete raw events and windows that have outlived their `keep`. In an exact stream they also delete identifiers older than the repeat window, and raw events whose period has been closed for longer than `raw until closed + D` allows. Anomalies, precomputed answers and refusal counts are never deleted.

## Names and Durations

Names start with a lowercase letter and use lowercase letters, digits and `_`, up to 48 characters. SQL words and the column names the compiler adds (`ts`, `n`, `w`, `b`, `res`, `value`, `period` and a few more) are refused with a clear message. Durations are a whole number and a unit: `s`, `m`, `h`, `d`, `w` (7 days) or `y` (365 days). Comments start with `#`.

## Limits of Version 0.2.0

- A compiled file keeps the policy it was created with. To change the policy, start a new file.
- Values are numbers and may not be null. Sketches assume positive values; zero and negatives go to one bucket read as 0.
- One anomaly rule per stream. Time is in seconds, periods in UTC.
- An exact stream's clock is the newest event time it has counted. One event stamped far in the future moves that clock for every sender and can close a period early, so exact streams need senders whose clocks are right.
- The SQL runs in SQLite 3.35 or newer with the math functions, which is the default build and the one in browsers, Python and most systems.

## Grammar

```text
policy      = { stream | precompute | quota | logs } .
stream      = "stream" name [ "exact" ] [ "from" "logs" [ "where" cond { "and" cond } ] ] "{" { line } "}" .
cond        = name "=" text .
logs        = "logs" "{" { "format" text | "mask" text | "templates" { "similarity" number | "depth" int } } "}" .
line        = "key" name [ "text" | "integer" ]
            | "value" name [ "real" | "integer" ]
            | "derive" name "=" expr
            | "id" name [ "text" | "integer" ] "refuse" "repeats" duration
            | "late" duration
            | "period" ( "hour" | "day" | "month" ) "close" duration
            | "raw" "keep" ( duration | "forever" )
            | "raw" "until" "closed" [ "+" duration ]
            | "rollup" duration "keep" ( duration | "forever" ) [ "quantiles" name { "," name } ]
            | "samples" int "per" duration
            | "anomalies" name { "log" | "change" } "z" ">" number { "memory" int | "warmup" int | "keep" int "per" duration }
            | "quantile" "accuracy" percent .
precompute  = "precompute" name "=" function "(" name [ "." name ] ")"
              [ "by" name { "," name } ] [ "per" ( "hour" | "day" | "month" ) ] .
quota       = "quota" name "=" name .
function    = "count" | "sum" | "avg" | "min" | "max" | "first" | "last" | "p" digits .
expr        = term { ( "+" | "-" ) term } .
term        = factor { ( "*" | "/" ) factor } .
factor      = number | name | "-" factor | "(" expr ")" .
duration    = int ( "s" | "m" | "h" | "d" | "w" | "y" ) .
text        = '"' { any character but a line end; \" is a quote } '"' .
```
