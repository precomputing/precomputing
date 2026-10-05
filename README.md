# Precomputing 0.1.1-alpha

Answers kept ready as data arrives.

Precomputing is a small policy language for streams of events, with two runtimes that keep its answers current in a SQLite file. A policy names the streams, the answers you want ready and how long each level of detail should live. Counts, sums, percentiles, window summaries, samples and unusual events then stay current as events arrive, and old detail fades on schedule. Any SQLite tool reads the file.

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

precompute p99_ms = p99(latency.ms) by endpoint
```

This is version 0.1.1-alpha, released on 5 October 2026. It has been checked on simulated and generated data; nobody has run it on production traffic yet. It hasn't had an outside security review either, so don't put it in front of anything that matters.

The whole project is open source under the Apache License 2.0. Copyright 2026 Precomputing.com.

## Install

Ready-made binaries for Linux are in [`releases/v0.1.1-alpha/`](releases/v0.1.1-alpha/): `precomputing-0.1.1-alpha-linux-amd64.tar.gz` for x86-64 and `precomputing-0.1.1-alpha-linux-arm64.tar.gz` for 64-bit ARM. They are static and have no dependencies. Check the tarball against `SHA256SUMS` before you unpack it:

```sh
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf precomputing-0.1.1-alpha-linux-amd64.tar.gz
./precomputing-0.1.1-alpha-linux-amd64/precomputing version
```

To build it yourself, see Quick Start.

## One Language, One File

| Part | What it is |
|---|---|
| SQL runtime | The compiler turns a policy into plain SQLite: tables, views and one trigger per stream. Events go in with `INSERT`. It runs in any SQLite 3.35 or newer with the math functions, the default build, including in the browser |
| Engine | One Go binary with SQLite built in. It runs the same policy in memory and writes the same file at every checkpoint, 12 to 17 times faster than the triggers on Demo 2's trades. Senders number their events, so a crash loses nothing and counts nothing twice |
| Meter | Exact streams for billing. A retried request counts once, a late report counts in the hour it happened, a month closes, and a quota is a view |
| Logs | A log reducer. It learns the templates of log lines, keeps every line on site for 48 hours and sends upstream the answers a dashboard shows. A dashboard definition can be imported as a policy |
| Traces | A store for the model calls of AI agents. It keeps each message and tool list once, under the SHA-256 of its bytes, masks secrets first, rebuilds any call byte for byte, and meters every call by run, repository, model and source, with budgets |
| MCP | An MCP server in `precomputing serve` and `precomputing mcp`: AI agents read any file's answers through five tools that only read, with read and write tokens for HTTP |

The two runtimes write the same file, value for value. Either one can carry on in a file the other wrote.

## Quick Start

To build from source you need Go 1.24 and a C compiler; SQLite comes with the source.

```sh
go build -o precomputing ./cmd/precomputing

# The SQL runtime: the compiled policy inside any SQLite database
./precomputing compile examples/latency.precompute | sqlite3 latency.db
sqlite3 latency.db "INSERT INTO latency (ts, endpoint, ms) VALUES (1790586000, '/api/search', 31.5)"
sqlite3 latency.db "SELECT * FROM p99_ms"

# The Engine and the Meter: seq, ts, request id, customer, model, gateway, tokens in and out
printf '1,1790290800,r-81f2,harbor,large,eu,2410,880\n' |
  ./precomputing put --policy examples/usage.precompute --seq usage.db
./precomputing get usage.db tokens_month

# Logs: a dashboard in, a policy out, then the lines
./precomputing import examples/shop-dashboard.json > shop.precompute
./precomputing put --lines --policy shop.precompute shop.db < app.log
./precomputing templates shop.db

# Traces: agent runs in, each call kept once and metered; any call back out
./precomputing traces --policy examples/traces.precompute traces.db < demo/traces/app/data/day.jsonl.gz
./precomputing traces --rebuild 'generated-0046#67' traces.db

# MCP: a file served to AI agents, read only, with a token file of lines such as "read TOKEN name"
./precomputing serve --read-only --token-file tokens usage.db     # MCP at http://localhost:8080/mcp
./precomputing mcp shop.db                                        # over stdio, for agents that start local tools
```

`precomputing serve` takes the same events over HTTP. `precomputing demo` runs Demo 2's whole trading day on your machine and checks it.

## The Eight Demos

All eight run at [precomputing.com/demo](https://precomputing.com/demo/). Each demo runs in the browser with the real code: SQLite built for WebAssembly, and this repository's Go program built for WebAssembly. Each one checks its answers against a recount made by separate code that never reads the file, at the end of a run or, on the live stream, every 30 seconds.

| Demo | What it runs | Measured |
|---|---|---|
| [SQL](https://precomputing.com/demo/sql/) | Three hours of API traffic through a compiled policy | 1,080,198 requests. The file is 4.5 MB against 35.2 MB of raw rows; counts and averages are exact and every p99 is within 0.65% |
| [Engine](https://precomputing.com/demo/engine/) | A trading day of stock trades turned into candles, with a pulled plug and a race against the triggers | 4,048,210 trades. Every closed candle matches a recount, also after the plug is pulled in the middle of a checkpoint |
| [Meter](https://precomputing.com/demo/meter/) | A month of AI usage from six customers, metered and billed | 373,351 reports, of which 7,219 retries and 642 reports after the close are refused. $5,804.56 of invoices match a recount to the billionth of a dollar |
| [Logs](https://precomputing.com/demo/logs/) | Two hours of a web shop's logs and a six-panel dashboard | 281,164 lines and 19 templates. 120 times fewer bytes go upstream, and every panel point is checked against a recount of the lines: counts and sums equal, p95 within 1% |
| [Wikipedia](https://precomputing.com/demo/live/) | Every change to every Wikimedia wiki from Wikimedia's public stream, live, through the Engine in the page | Checked every 30 seconds as changes arrive. On 75 minutes of simulated changes through the same code, 112,985 changes and 99,992 of 99,992 counts and sums equal to a recount |
| [Incident Déjà Vu](https://precomputing.com/demo/dejavu/) | A day of the web shop's logs with six incidents written down, then two more hours checked every ten seconds against them | The three kinds from the day before named right 8 to 10 seconds after they began, the new kind flagged and then recognized once written down, no false alarms; 80 of 80 named right on 20 more random days |
| [MCP](https://precomputing.com/demo/mcp/) | An AI agent's four questions to the files of Demos 1 to 4, over MCP, and a recorded session with a real agent | 7 calls and 4 file descriptions: 2,561 tokens, against about 5.3 million for the raw data behind the answers. 19 of 19 checks against the raw events |
| [Traces](https://precomputing.com/demo/traces/) | A day of 100 generated coding-agent runs on 30 repositories, with a stuck run, planted secrets and daily budgets | 2,045 calls: 90.9 MB as sent, kept in 6.5 MB. Every call rebuilt byte for byte, 4 secrets masked, every cost equal to a recount |

`node tools/run-demo1.mjs` to `run-demo8.mjs` repeat the runs headless with each demo's own code and print these numbers. Demo 5's run uses simulated changes, since a live stream can't be replayed; `node tools/wiki-sse.mjs` serves such changes the way `stream.wikimedia.org` does, to test the page without the network. `node tools/build-dejavu-history.mjs` makes the file Demo 6 starts from.

Demo 8's runs are generated by `tools/traces-generate.mjs` in the fields of [nebius/SWE-rebench-openhands-trajectories](https://huggingface.co/datasets/nebius/SWE-rebench-openhands-trajectories), a public dataset of real agent runs, with invented repositories, issues and code. To run the demo on real runs, read a sample out of one of the dataset's Parquet files and prepare it; the page then shows the dataset's credit line, which its CC BY 4.0 license requires:

```sh
pip install pyarrow
python3 tools/traces-from-parquet.py FILE.parquet --sample 100 > build/traces/nebius.jsonl
node tools/traces-prepare.mjs build/traces/nebius.jsonl --source nebius   # writes demo/traces/app/data/
```

## Documentation

- [The policy language](docs/language.md)
- [The file format](docs/file-format.md)
- [The Engine](docs/engine.md)
- [The Meter](docs/meter.md)
- [Logs](docs/logs.md), with Traces for agent calls

## Repository

| Path | What it holds |
|---|---|
| `policy/` | Reads and checks `.precompute` files |
| `compile/` | Turns a policy into SQLite; `testdata/` holds the expected output for every example |
| `engine/` | The Engine: streams in memory, checkpoints and reload. `engine/sqlitestore/` compares it with the compiled triggers |
| `logs/` | The log reducer and the dashboard import |
| `internal/drain/` | The Drain template miner, checked against the published Loghub results |
| `internal/sqlite/` | SQLite 3.53.4, compiled into the binary, and a small binding |
| `mcp/` | The MCP server: its five tools, the 2026-07-28 revision and the older handshake, over HTTP and stdio |
| `traces/` | The store of agent calls: pieces by SHA-256, calls rebuilt with SQL, secrets masked, every call metered |
| `cmd/precomputing/` | The command line and the HTTP server, with tokens |
| `wasm/precomputing/` | The compiler, the Engine, the reducer, the import, the MCP server and the store of agent calls, built for the browser |
| `examples/` | Example policies and Demo 4's dashboard |
| `demo/` | The eight live demos, as served at precomputing.com/demo/; `demo/mcp/recording/` holds the raw log of the recorded agent session |
| `tools/` | Builds, headless demo runs, the crash lab, the file comparison, the mutants and the full verification run |
| `docs/` | The documentation |
| `releases/` | Ready-made Linux binaries, with checksums and release notes |
| `results/` | The outputs of the checks run for this release |

## Checks

```sh
go test ./...                                        # parser, compiler, Engine against the triggers, reducer, Drain, MCP, Traces
(cd tools && npm install)                            # SQLite WebAssembly, the MCP clients and the tokenizer, for the headless runs
./tools/build-demos.sh                               # demo policies, the WebAssembly build, SQLite WebAssembly
node tools/run-demo1.mjs                             # and run-demo2.mjs to run-demo8.mjs
node tools/run-demo7.mjs --native build/precomputing # Demo 7's calls to the native server too, compared to the byte
node tools/mcp-check.mjs build/precomputing FILE     # both official MCP clients, over HTTP and stdio
node tools/run-demo8.mjs --db b.db --native build/precomputing   # Demo 8, then the native file compared with it
python3 tools/check_native.py build/precomputing     # the example policies in native SQLite, 31 checks
python3 tools/mutants.py                             # three small changes to the Engine, each caught by the tests
python3 tools/meter-triggers.py build/precomputing reports.csv   # Demo 3 through the triggers in native SQLite
go run ./tools/logbits | node tools/logbits.mjs      # ln() in SQLite WebAssembly against Go, bit for bit
go run ./tools/crashlab                              # the Engine killed 100 times while it takes in a day
go run ./tools/filecompare a.db b.db                 # two files compared table by table, value by value
sh tools/fetch-loghub.sh && LOGHUB=build/loghub go test -run TestLoghub -v ./internal/drain
sh tools/verify.sh                                   # all of the above, with each output kept in build/verify
```

To try the demos locally, serve the `demo` folder (`python3 -m http.server --directory demo`) and open `/sql/app/`, `/engine/app/`, `/meter/app/`, `/logs/app/`, `/live/app/`, `/dejavu/app/`, `/mcp/app/` or `/traces/app/`. The Wikipedia demo also takes `?stream=` with the address of another stream, such as the one `tools/wiki-sse.mjs` serves.

## Status

Version 0.1.1-alpha. The language, both runtimes, the Meter and Logs are complete for what they cover, and 0.1.1 adds the MCP server, tokens for the HTTP server and Traces. Each document ends with the limits of this release. Logs has no forwarder yet: its batches are read from the file with SQL. The HTTP server takes tokens but has no TLS. Traces takes whole runs; a hook in the agent's path is not built yet. An OpenTelemetry Collector plug-in and TLS are on the roadmap.

## License

The whole project is open source under the Apache License 2.0. Copyright 2026 Precomputing.com. See [LICENSE](LICENSE) and [NOTICE](NOTICE). Everything in this repository is under Apache 2.0 unless a file says otherwise. Third-party components keep their own licenses: NOTICE lists them, and [THIRD-PARTY-LICENSES.txt](THIRD-PARTY-LICENSES.txt) has the full texts.

To report a security problem, see [SECURITY.md](SECURITY.md). Anything else: info@precomputing.com.
