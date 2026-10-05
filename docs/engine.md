# The Engine

The Engine runs a policy in memory and keeps the same SQLite file the compiled SQL keeps. It is one Go binary with SQLite built in: a command line, an HTTP server that also serves AI agents over MCP, and the same code built for the browser. Where the SQL runtime does its work inside SQLite on every insert, the Engine does it in Go and writes what changed to the file every so often. The answers are the same, to the last bit, and on Demo 2's trades it is 12 to 17 times faster.

```sh
go build -o precomputing ./cmd/precomputing
./precomputing put --policy examples/trades.precompute --seq trades.db < trades.csv
./precomputing get trades.db last_price
```

## How It Works

Every event updates state held in memory: the window summaries of each rollup, sketch buckets, samples, the anomaly baseline of its key and the rows behind each precompute. A checkpoint writes everything that changed since the last one to the file in a single transaction, together with the sequence number of the last event it covers, and then runs the policy's distill statements. When a checkpoint returns, every event applied so far is in the file.

The Engine reads its file in three cases only: when it opens (the sequence numbers of its senders and the newest window of every key), when an event touches a window older than the newest one it knows (a late event, or the window it was filling when it stopped), and the first time it meets a key's baseline or a precompute row. Closed windows leave memory after the checkpoint that wrote them, so memory holds the open windows of active keys, one baseline per key and the precompute rows of the current period.

Raw events that the next distill would delete are not written at all. With a checkpoint every fraction of a second this never happens; it saves work when a backlog is replayed with rare checkpoints.

## Sequence Numbers and Crashes

Each event may carry a sequence number from its sender, counted from 1. The Engine remembers the last one it applied from each sender and skips any event whose number is not above it. The file records the last number it holds, in `_precomputing_sources`, in the same transaction as the rows.

So the rule for a sender is simple. Keep every event until a checkpoint acknowledges it. After a crash, ask the Engine where the file stands (`put` prints `ready SEQ` when it starts) and send again from `SEQ + 1`. Nothing is lost, and nothing is counted twice.

A crash in the middle of a checkpoint leaves no trace: SQLite rolls the unfinished transaction back when the file is next opened, and the file holds exactly the last complete checkpoint.

## The Same Answers as the SQL Runtime

Every update follows the compiled trigger step by step, with SQLite's arithmetic in the same order: integers stay exact integers as SQLite keeps them, ties in minimum and maximum go the way SQLite's `min` and `max` send them, and the first and last values follow the same rules. The one function whose last bit depends on the platform is the logarithm behind `ln()`, used by sketches and by `log` anomalies. The native Engine calls the C library's `log`, the one the SQLite inside the same binary uses. In the browser, SQLite's WebAssembly build computes `ln()` with the FreeBSD-derived logarithm that Go's `math.Log` also uses; across two million test values the two agree on every bit (`go run ./tools/logbits | node tools/logbits.mjs`).

The tests check this directly. The same events go through the compiled triggers and through the Engine, with late events, zeros, a price jump and a day boundary among them, and the two files are compared table by table, value by value. They match. Small changes to the Engine's rules, such as moving the last value of a window on a tie or starting the baseline's weighting one event late, make the comparison fail, so it is a real check. `python3 tools/mutants.py` makes three such changes, each in a copy of the code, and shows that the test catches every one.

The triggers are in the Engine's file too. The SQL runtime can carry on in a file the Engine wrote, with plain `INSERT` statements, and the Engine can open a file the triggers filled and carry on from it. The tests hand a file over both ways and compare it with a file made by the triggers alone. One file should have one writer at a time.

## Command Line

| Command | What it does |
|---|---|
| `put [--policy P] [--seq] [--format csv\|json] [--every 100ms] FILE` | Reads events from standard input and keeps the file current. Prints `ready SEQ` when it starts and `ok SEQ` after each checkpoint |
| `serve [--policy P] [--addr localhost:8080] [--every 100ms] [--read-only] [--token-file F] FILE` | The same over HTTP, with MCP for AI agents at `/mcp`. `--read-only` serves a file without taking events; `--token-file` asks every request for a token |
| `mcp FILE` | Serves one file to an AI agent over standard input and output, for agents that start their tools themselves |
| `get FILE NAME\|SQL [--json]` | Prints a precompute, a table or a read-only query |
| `stats FILE` | Rows and bytes per table, and where each sender stands |
| `inspect FILE` | The policy the file was made from and what it holds |
| `put --lines [--policy P] FILE` | Reads log lines with the policy's `logs` block, learns their templates and feeds the streams from logs. A line's sequence number is its line number (see [Logs](logs.md)) |
| `import DASHBOARD.json` | Prints a policy that keeps a dashboard's panels ready from log lines |
| `templates FILE` | The log templates a file has learned |
| `demo` | Runs Demo 2's trading day through the Engine on this machine and checks it |
| `compile [--distill] POLICY` | Prints the SQL for the SQL runtime |
| `traces [--policy P] [--again RUNS] FILE < runs.jsonl` | Keeps the model calls of AI agent runs, each piece once, and meters them (see [Logs](logs.md) and [the Meter](meter.md)) |
| `traces --rebuild CALL [--reply] FILE` | Prints one call's request, rebuilt from the file byte for byte, or its reply |

CSV lines hold `[seq,] ts, keys..., values...` in the order the policy declares them. JSON lines name their fields: `{"seq": 1, "ts": 1790602200, "symbol": "SIM1", "price": 187.41, "size": 100}`. A new file needs `--policy`; an existing file uses the policy it carries. `--sync full` (the default) waits for the disk at every checkpoint, so an acknowledged event survives a power cut; `normal` survives a crash of the process but not of the machine.

## HTTP

| Request | Answer |
|---|---|
| `POST /v1/events?source=NAME` | Events, one per line: JSON, or CSV with `Content-Type: text/csv`. The reply comes once they are in the file: `{"applied", "skipped", "refused", "committed"}` |
| `GET /v1/answers/NAME` | The rows of a precompute, as JSON |
| `GET /v1/query?sql=...` | A read-only query, as JSON |
| `GET /v1/stats` | Counters and senders |
| `POST /mcp` | The Model Context Protocol for AI agents: see [Serving AI Agents over MCP](#serving-ai-agents-over-mcp) |

Reads go through a second, read-only connection and see the last checkpoint. With `--token-file`, every request needs a token, as described below; the server speaks plain HTTP, so outside localhost it belongs behind a proxy that adds TLS.

## Serving AI Agents over MCP

`precomputing serve` answers the Model Context Protocol at `/mcp`, beside the HTTP API, so an AI agent can read the file's answers. `precomputing mcp FILE` serves one file over standard input and output, for agents that start their tools as local processes. Both run the same server (`mcp/`), and so does the page of Demo 7.

```sh
precomputing serve --read-only --token-file tokens usage.db
precomputing mcp shop.db
```

Every tool only reads. An answer comes back as short CSV text under a line that says what was read, so a model can use it as it is:

| Tool | What it returns |
|---|---|
| `describe_file` | What the file keeps: its streams, keys and values, the time span of its events, its precomputes, what is kept whole and for how long |
| `get_answer` | The rows of a precompute for a key and a period, such as one customer's invoice for September. At most 500 rows |
| `get_windows` | Window summaries of a stream over a time range, read from the finest rollup that covers it: counts, sums, averages, minimums, maximums and percentiles, by key or in all. At most 1,000 rows |
| `get_kept` | Events kept whole: raw events, samples, anomalies or log templates, newest, oldest or most unusual first, filtered by keys or by text. At most 200 rows |
| `query` | One read-only SQL statement, stopped after five seconds. At most 1,000 rows |

**The protocol.** The server speaks the 2026-07-28 revision: each request is complete on its own, with the protocol version and the client's capabilities in its `_meta`, and carries the headers the revision asks for, `MCP-Protocol-Version`, `Mcp-Method` and, for a tool call, `Mcp-Name`. A request that breaks a rule gets the error the revision prescribes. The server also answers clients that open with the older handshake of 2025-11-25, 2025-06-18 or 2025-03-26. Its tool list says it may be cached for an hour.

**Tokens.** With `--token-file`, every request needs `Authorization: Bearer TOKEN`. The file holds one token a line, `read TOKEN` or `write TOKEN`, with an optional name for the logs. A token has at least 16 characters, and the server keeps only its SHA-256. A read token may read and use `/mcp`; a write token may also post events. `--read-only` serves a file without taking events at all. `/mcp` allows 20 tool calls a second for each token or address (`--rate`), and answers a web page from another origin only when `--allow-origin` names it.

| Check | Result |
|---|---|
| Official clients | The MCP TypeScript client, version 2.2.0 on the 2026-07-28 revision and version 1.31.0 on the older handshake, over HTTP and over standard input and output: every combination passes on the four files of Demo 7 (`node tools/mcp-check.mjs build/precomputing FILE`) |
| Native against the browser | The same 11 tool calls and 4 tool lists to `precomputing mcp` and to the WebAssembly build of Demo 7: identical to the byte (`node tools/run-demo7.mjs --native build/precomputing`) |
| Tokens | A request without a token gets 401; events posted with a read token get 403; `--read-only` refuses events with every token |
| A real agent | Claude, through the official client over HTTP with a read token, answered four questions of its own in 18 calls. Demo 7 shows the recording, and `demo/mcp/recording/` holds its raw log |

## In the Browser

The same Go code, with the compiler, the log reducer, the dashboard import, the MCP server and the store of agent calls, is built for WebAssembly: `precomputing.wasm`, 5.9 MB, 1.6 MB compressed. A small bridge (`demo/lib/engine.js`) gives it the page's SQLite WebAssembly build as its file: a checkpoint travels as one block of bytes and is written in one transaction with prepared statements. The MCP server reads through one function over the page's SQLite, one read-only statement at a time and stopped after five seconds, as the native server reads through a read-only connection. Demos 2, 4, 5 and 6 run this way.

## Measured

On the machine that built this release, a shared two-core cloud server (Intel Xeon at 2.8 GHz). Speeds there change from one day to the next, in some runs by a third or more, so they are given as the range of our runs:

| Run | Result |
|---|---|
| `precomputing demo`: the whole trading day, 4,048,210 trades, into a file on disk with a full sync every 10,000 trades | 12 to 16 seconds in our runs, 258,000 to 337,000 trades a second. Every closed candle and the quote board checked against a recount of every trade: identical |
| The first 200,332 trades through the Engine and through the compiled triggers | 450,000 to 570,000 against 26,700 to 37,000 trades a second, 15 to 17 times faster. The two files: 122,793 rows, 576,218 values, identical |
| The crash lab: `precomputing put` killed without warning 100 times while it takes in the day | Over 40 kills in the middle of a checkpoint (42 and 45 in two runs). Acknowledged trades lost: 0. The final file is identical to an uninterrupted run and to the compiled triggers: 156,320 rows, 1,296,775 values |
| In Chromium, on the same machine, the Engine in WebAssembly | 170,000 to 220,000 trades a second while writing its file five times a second. Back from a pulled plug in about 0.2 seconds. In the race against the triggers, 12 to 15 times faster, with identical files |
| Demo 5 in Node: 75 minutes of simulated Wikipedia changes through three streams, one of them keyed by article and person | 112,985 changes in 5.3 to 5.5 seconds of Engine time, about 20,000 a second, far above the live stream's tens a second. The native Engine's file from the same changes is identical: 1,596,566 values |

The crash lab is `go run ./tools/crashlab`. It feeds the day as CSV, kills the Engine with SIGKILL at random moments between 40 and 300 milliseconds apart, restarts it, checks that the file never holds less than the last acknowledgement, resends from where the file stands, and at the end compares the file with an uninterrupted run and with the triggers.

## Limits of Version 0.1.1

- One writer per file. Reads can run alongside.
- Values must be finite numbers; integer values and keys must be whole numbers. In the browser, integers travel through the bridge as doubles, exact below 2^53.
- The Engine keeps one baseline per key and the current period's precompute rows in memory, and at start it reads the newest window of every key.
- A policy's streams, rollups and precomputes are fixed for the life of a file.
- The HTTP server takes tokens, but it has no TLS. Outside localhost, run it behind a proxy that adds TLS.
