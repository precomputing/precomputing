# Logs

The log reducer sits next to the services that write the logs. It reads each line, learns what kind of line it is, keeps every line in a local file for 48 hours, and keeps ready the answers a dashboard shows. Upstream, to Datadog, Splunk or whatever holds the dashboards, go those answers, the error lines, a few examples and any new kind of line. The raw lines stay home, where they can still be searched.

It is the Engine with a front end for log lines, driven by an ordinary policy. The policy can be written by hand or made from a dashboard. Version 0.1.1 adds a second front end, for the model calls of AI agents: see [Agent Traces](#agent-traces).

```sh
precomputing import examples/shop-dashboard.json > shop.precompute
precomputing put --lines --policy shop.precompute shop.db < app.log
precomputing templates shop.db
```

## From a Dashboard to a Policy

The importer reads a small part of a dashboard definition: the log format, and panels that count lines or add up, average or take percentiles of a field, filtered by fields and grouped by others. Demo 4's dashboard has six panels:

```json
{
  "title": "Web shop",
  "logs": { "format": "<timestamp> <level> <service> <message>" },
  "panels": [
    { "title": "Requests a minute by route", "where": { "service": "web" }, "measure": "count", "by": ["route"] },
    { "title": "p95 latency by route", "where": { "service": "web" }, "measure": "p95", "field": "ms", "by": ["route"] },
    { "title": "Errors a minute by service", "where": { "level": "ERROR" }, "measure": "count", "by": ["service"] },
    { "title": "Payments by provider and result", "where": { "service": "payments" }, "measure": "count", "by": ["provider", "result"] },
    { "title": "Revenue a minute", "where": { "service": "checkout" }, "measure": "sum", "field": "total" },
    { "title": "Top searches", "type": "toplist", "where": { "service": "search" }, "measure": "count", "by": ["q"], "top": 10 }
  ]
}
```

Each panel becomes a stream from logs with a 1-minute rollup. Panels that filter, group and measure alike share a stream. A percentile adds a quantile sketch and keeps unusual values whole. Two streams are always added: every line kept on site for 48 hours and counted per template, and error lines kept whole, up to five a minute of each kind. Part of the result:

```text
logs {
  format "<timestamp> <level> <service> <message>"
}

stream lines from logs {
  key    service text
  key    level text
  key    template integer
  raw    keep 48h
  rollup 1m keep 30d
  rollup 10m keep 30d
  samples 1 per 10m
}

# "p95 latency by route": p95 of ms
stream web_ms_by_route from logs where service = "web" {
  key    route text
  value  ms real
  rollup 1m keep 30d quantiles ms
  anomalies ms log z > 4 keep 5 per 1m
}
```

The whole policy is `examples/shop.precompute`. It is a starting point: edit it like any other policy.

## Reading Lines

The `logs` block says how lines are read. `format` names the fields of a line in order; the text between fields is a regular expression, and a run of spaces matches any run of white space. The format needs a `<message>` and the time of the line, as a `<timestamp>` or as `<date>` and `<time>`. RFC 3339 times, `2026-09-29 12:00:00.123` and seconds since 1970 all read. A line that does not match, or whose time does not read, is counted as not read and skipped.

A line's fields are the format's fields, every `key=value` pair in the message (a value in double quotes may hold spaces), and `template`, the number of the line's template. A stream from logs takes a line when the line meets its `where` and has every key and value the stream declares; values must be numbers. The line itself is kept with the stream's raw events, samples and anomalies, in a column named `line`.

`mask "REGEX"` lines in the `logs` block mask more of the message before its template is learned, such as identifiers that are not written as `key=value`. `templates similarity 0.5 depth 4` sets the template tree; those are the defaults.

## Templates

Templates are learned with Drain, the online log parser with a fixed-depth tree (He, Zhu, Zheng and Lyu, 2017). The Go port follows the reference implementation in logpai/logparser step by step. On each of the 16 Loghub samples of 2,000 lines, with the published settings, its grouping accuracy equals Drain's published figure:

| Dataset | Accuracy | Dataset | Accuracy |
|---|---|---|---|
| HDFS | 0.9975 | Linux | 0.6900 |
| Hadoop | 0.9475 | Android | 0.9110 |
| Spark | 0.9200 | HealthApp | 0.7800 |
| Zookeeper | 0.9665 | Apache | 1.0000 |
| BGL | 0.9625 | Proxifier | 0.5265 |
| HPC | 0.8870 | OpenSSH | 0.7875 |
| Thunderbird | 0.9550 | OpenStack | 0.7325 |
| Windows | 0.9970 | Mac | 0.7865 |

The average is 0.8654, the published figure. `sh tools/fetch-loghub.sh` downloads the samples and `LOGHUB=build/loghub go test -run TestLoghub -v ./internal/drain` repeats the check.

In the reducer, values of `key=value` pairs are masked before a line's template is learned, so `charge ok provider=northpay amount=59.90` becomes `charge ok provider=<*> amount=<*>`. Lines of different services or levels never share a template. The templates live in the file, in `_precomputing_templates`, with the tokens each one started from; after a restart the reducer rebuilds its tree from them exactly as it was.

## What Goes Upstream

Demo 4 sends upstream, every minute, the rows of each panel's 1-minute window, the error lines kept whole, the unusual requests and, every ten minutes, one example of each template. A new template goes at the next checkpoint, at most five seconds after its first line; a new kind of WARN or ERROR line is an alert. The dashboard is then drawn from what was sent alone, and each point is checked against a recount of the raw lines.

In version 0.1 the batches are read from the file with SQL, as the demo does; `precomputing get` reads any table the same way. A forwarder that sends them on, and an OpenTelemetry Collector plug-in, are on the roadmap.

## Measured

Demo 4 runs two hours of an invented web shop, about 39 lines a second on average: five services, a payment provider failing for eight minutes at 12:40, and a deploy at 13:05 that makes search log three debug lines with every query. The numbers come from `node tools/run-demo4.mjs`, which runs the demo's own code with the page's SQLite WebAssembly build, and from the native binary on a shared two-core cloud server (Intel Xeon at 2.8 GHz), where speeds change from one day to the next, in some runs by a third or more; where runs differed, the table gives their range.

| What | Result |
|---|---|
| The two hours | 281,164 lines, 25.5 MB, 19 templates |
| Sent upstream | 212 KB in 124 batches and 7,896 events: 120 times fewer bytes and 36 times fewer events than the lines |
| The dashboard drawn from what was sent | Every point checked against a recount of the raw lines: 6,231 counts and sums identical, 840 p95 values within 1%, the top 10 searches identical |
| The payment incident | Caught as a new ERROR template 2 seconds after its first line |
| At Datadog's list prices | About $173 a month with every line upstream and $4.84 with what was sent, at $0.10 per GB ingested and $1.70 per million events indexed for 15 days, billed annually (checked 29 September 2026). Everything sent is counted as log events |
| Kept on site | All 281,164 lines in a 39.8 MB file; a search for "northpay" over all of them takes 0.1 to 0.2 seconds |
| Speed | 31,000 to 43,000 lines a second through the reducer and the Engine in WebAssembly (Node). Natively, `precomputing put --lines` takes the same file in 1.8 to 2.6 seconds, 108,000 to 156,000 lines a second with a full sync at every checkpoint |
| Native and browser files | The same 352,525 rows. Of 1,794,042 values one differs: an anomaly baseline 2 units apart in the last place, because the two builds' logarithms differ in the last bit |

`node tools/run-demo4.mjs --lines shop.log --db browser.db` writes the lines and the browser's file, `precomputing put --lines` makes the native one, and `go run ./tools/filecompare` compares them. The tests in `logs/` run the same reducer against the compiled triggers (the two files match value for value) and stop it without warning again and again (the file ends up exactly as a run that never stopped, templates included).

## Incident Déjà Vu

Demo 6 puts the counts to a second use: telling whether an incident has happened before. The policy is one stream from logs that counts every template by service and level, a minute at a time for a week and an hour at a time for 90 days, and keeps every line for ten minutes. The comparison is plain SQL in the same file (`demo/dejavu/app/dejavu.sql`):

- **What stands out.** A template stands out when its rate over the last minute or two is at least three times its usual minute, higher or lower, and at least six lines a minute away. Usual is the median minute of the hour before, so an earlier incident in that hour doesn't move it. A template going quiet must stay quiet over one minute more. DEBUG lines are left out.
- **The fingerprint.** Each template that stands out is scored with the logarithm of the change.
- **Writing it down.** An insert into `incidents` fires a trigger that saves the fingerprint of the incident's first two minutes in `incident_keys`, with the newest raw line of each template while the raw lines are kept. The saved fingerprint outlives the minute windows.
- **The comparison.** The view `deja_vu` ranks every incident written down by the cosine of its fingerprint and now's. At 0.7 or more now looks like it; at 0.4 or more, partly. When two incidents score within 0.05 of each other, the page compares the words the templates hide in the newest raw lines with the example lines saved with each.

The file the demo starts from holds 24 hours of the shop, 2,133,322 lines of 25 templates, with six incidents written down. `tools/build-dejavu-history.mjs` makes it with the demo's own code, and making it again gives the same bytes. The numbers below come from `node tools/run-demo6.mjs`, which runs the page's code on the same SQLite WebAssembly build:

| What | Result |
|---|---|
| Today's two hours | 242,635 lines, five incidents. The three kinds from the day before named right 8 to 10 seconds after they began. The new kind flagged as new after 5 seconds, written down at 13:22 and named 10 seconds into its return at 13:48 |
| Wrong names and false alarms | None, in 721 checks |
| The day before, every minute | 1,379 minutes checked. Something stood out in 47, all during the six incidents |
| 20 more days, incidents at random | Known kinds named right 80 times out of 80, after 12 seconds at the median and 50 at most. New kinds flagged 20 times out of 20. None named wrong, no false alarms |
| A check | The two views read in 4 to 5 ms on average, 35 ms at most |
| Speed | 67,000 to 84,000 lines a second through the reducer and the Engine in Node, 58,000 to 67,000 in Chromium |
| Counts | All 1,661 counts by minute and hour for each service and level equal to a recount of the lines |
| Native and browser files | `precomputing put --lines` over the same 2,375,957 lines: 5 tables, 45,366 rows, 328,955 values, no difference |

Building Demo 6 found one bug. Reopening a log file in the browser failed, because the browser's store hands every number to Go as a double and the reducer expected integers when it read its templates back. The reducer now takes either, and every run of Demo 6, in Node or in a browser, goes through that path when it opens the file.

## Agent Traces

An AI agent works by calling a model again and again, and every call sends the whole conversation so far: the system prompt, the tool list, the task, everything the agent has said and every tool output it has read. A run's 40th call repeats its first 39, so a tracer that stores each call whole stores the start of every run many times over. The store of agent calls (`traces/`) keeps each message and each tool list once, and meters every call through the policy's exact streams (see [the Meter](meter.md#metering-agent-calls)).

```sh
precomputing traces --policy examples/traces.precompute traces.db < runs.jsonl
precomputing traces --rebuild 'generated-0046#67' traces.db
precomputing get traces.db "SELECT * FROM repo_budget"
```

**What the file holds.** Beside the policy's tables the store adds `trace_pieces`, each message or tool list once, as sent, under the SHA-256 of its bytes, and `trace_calls`, each call as the list of its pieces, with its time, run, repository, model, size, tokens and the SHA-256 of the whole request. The view `trace_requests` puts any call back together with SQL alone. Both tables go into the file with the Engine's checkpoints, in the same transaction as the meter's rows, so a crash cannot leave a call without its cost or a cost without its call.

**Each call adds only what is new.** A run's calls grow at the end. For each run in progress the store remembers the pieces it has seen and the SHA-256 state of the request so far, so a call hashes only its new messages and the tool list, and stores only pieces the file does not hold yet, from this run or any other. The system prompt and the tool list of the demo's 100 runs are one piece each.

**Secrets.** Before a piece is hashed and stored, the store masks what matches its patterns, and a label such as `[redacted aws-secret]` takes the secret's place. The masked piece is what the file keeps and what a rebuilt call returns, so the secret never reaches the file.

| Kind | What it matches |
|---|---|
| `private-key` | A PEM block, from `-----BEGIN ... PRIVATE KEY-----` to its END line |
| `aws-key-id` | `AKIA` and 16 capitals or digits: an AWS access key id |
| `aws-secret` | 40 characters after `aws_secret_access_key` and `=` or `:`, in any case; the name stays |
| `github-token` | `ghp_`, `gho_`, `ghu_`, `ghs_` or `ghr_` and 36 letters or digits |
| `api-key` | `sk-` or `sk-proj-` and at least 32 letters, digits, `-` or `_` |
| `slack-token` | `xoxa-`, `xoxb-`, `xoxp-`, `xoxr-` or `xoxs-` and at least 10 more characters |
| `bearer-token` | At least 16 characters after `Authorization: Bearer`, in any case; the header stays |

The patterns keep to what the regular expressions of Go and of JavaScript read alike, so the page of Demo 8 can run them itself and check the file. Each pattern has a quick test first, such as a word every match contains, and a piece that fails every quick test needs no regular expression. A test masks 20,000 made-up texts, built from pieces of secrets, with and without the quick tests, and requires the same result.

**The runs.** `precomputing traces` reads runs as JSON lines, gzipped or not: each run's id, repository, model and start, its tool list as sent, and each message as sent with its source, tokens and time. `tools/traces-prepare.mjs` writes them from runs in the fields of [nebius/SWE-rebench-openhands-trajectories](https://huggingface.co/datasets/nebius/SWE-rebench-openhands-trajectories), a public dataset of real agent runs, counting tokens with the o200k_base tokenizer and laying the runs over one day. Demo 8 uses runs made by `tools/traces-generate.mjs` in the same fields, with invented repositories, issues and code. `tools/traces-from-parquet.py` reads real runs out of one of the dataset's Parquet files instead; a page that shows them credits the dataset, which Nebius publishes under CC BY 4.0.

Demo 8 runs a day of 100 such runs on 30 repositories through the store and the Engine in WebAssembly:

| What | Result |
|---|---|
| The day | 100 runs, 2,045 model calls, 4,190 messages |
| Bytes | The calls as sent: 90.9 MB. Kept: 4,092 pieces, 4.5 MB, and with the calls table and the indexes 6.5 MB on disk, 14 times smaller |
| Rebuilt | All 2,045 calls rebuilt with `trace_requests` are the requests as sent, byte for byte, against requests the page builds itself from the runs |
| Secrets | 4 planted in 3 runs and 4 masked, the same 4 the page finds with the same patterns; none left in the file |
| Speed | 875 to 989 calls a second through the store and the Engine in WebAssembly (Node) over four runs, with a checkpoint every minute of the day. Natively, the whole day in 0.8 to 1.0 seconds |
| Native and browser files | 19 tables, 40,043 rows and 231,641 values, the store's tables included: identical, leaving out the budget limits the demo adds |

`node tools/run-demo8.mjs --db browser.db --native build/precomputing` runs the demo's own code, rebuilds every call, recounts every cost, runs the same day through `precomputing traces` and compares the two files. The tests in `traces/` rebuild every call of the day from the file, check the patterns, and open a file again to carry on from where it stood.

## Limits of Version 0.1.1

- One line format per policy. JSON lines are read as text.
- Template numbers belong to one file. A template's text widens as lines join it; its number stays.
- The errors stream matches the level `ERROR` as written.
- The dashboard import reads count, rate, sum, avg, min, max and percentiles, grouped by fields and filtered by fields equal to a text.
- Upstream batches are read from the file; there is no forwarder yet, and no OpenTelemetry Collector plug-in.
- The shop, its customers' searches and its incidents are invented.
- Demo 6's thresholds were chosen on this shop. Two faults that break the same templates in the same proportions look alike, and overlapping incidents mix their fingerprints.
- Traces takes whole runs, from the command line or in the browser. A hook in the agent's path, so that each call arrives as its reply comes back, is not built yet.
- Secrets are masked by pattern. A secret of a shape the store has no pattern for is stored as it came.
- A run's calls must arrive in order, and a model's name must be plain ASCII.
- The agent runs in Demo 8 are generated, with invented repositories, issues and code.
