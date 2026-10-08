# Changelog

## 0.2.0 (8 October 2026)

Apart from one fix for ARM64, the code is 0.1.1-alpha's. What's new is mostly how it ships.

- Releases build themselves. Every push to `main` runs the tests on Linux (x86-64 and ARM64) and macOS, with the race detector on Linux. When they pass on a commit that raises the version, the release workflow builds the binaries, checks them, tags the commit and publishes the release. Nothing goes out unless every build and check passes.
- Binaries for five systems instead of two: Linux x86-64 and ARM64 (static, as before), macOS Apple silicon and Intel, and Windows x86-64. They're on GitHub's releases page under names without a version, so `releases/latest/download/` links always get the newest.
- `tools/smoke.sh` is the check each binary has to pass: a policy compiled, a file kept current from events and read back, then `precomputing demo`, Demo 2's 4,048,210 trades recounted with 0 differences allowed. The Windows binary runs the same check on Windows. The ARM64 Linux binary now runs before it's released; 0.1.1-alpha's was only built.
- Fixed: on ARM64 (Apple silicon, ARM Linux) the Engine's files drifted from the SQL runtime's in the last bit of some sums, and Demo 2's recount found 7,045 differing candles. Go may fuse a multiply and an add into one instruction there, which rounds once instead of twice. The Engine, the recount, the compiler and the MCP server now round each product first, as the market simulator already did, so ARM64 writes the same values as x86-64.
- The binaries are no longer kept in the repository; the `releases/` folder is gone.
- NOTICE and THIRD-PARTY-LICENSES.txt cover the mingw-w64 runtime in the Windows binary.

## 0.1.1-alpha (5 October 2026)

The first public release, under the Apache License 2.0.

- One code base. The 30 September update (the MCP server, tokens for the HTTP server and Traces, with their two demos) and the 1 October work (the Wikipedia and Incident Déjà Vu demos and a log reducer fix) were built in parallel from the 29 September code. They are merged here. The Go code merged without a conflict: the 1 October work changed one Go file, `logs/reducer.go`, which the 30 September update didn't touch.
- Each demo has one number: 1 SQL, 2 Engine, 3 Meter, 4 Logs, 5 Wikipedia, 6 Incident Déjà Vu, 7 MCP, 8 Traces. MCP and Traces were Demos 5 and 6 in the 30 September update; their headless runs are now `tools/run-demo7.mjs` and `tools/run-demo8.mjs`.
- One engine runs all eight demos: `demo/lib/go/precomputing.wasm`, 5.9 MB, 1.6 MB compressed. One stylesheet carries both sets of demo styles.
- The file Demo 6 starts from, `demo/dejavu/app/history.db.gz`, is rebuilt with 0.1.1. It holds the same lines and the same bytes apart from the version stamp, so Demo 6's native and browser files now match with no difference.
- Static release binaries for Linux, amd64 and arm64, in `releases/v0.1.1-alpha`.
- The checks run for this release are in `results/`: the Go tests of both lines of work, the eight demos run headless with the published figures, the native files compared with the browser's, and both official MCP clients.

## 0.1.1 (30 September 2026)

- An MCP server with five tools that only read, in `precomputing serve` at `/mcp` and over stdio as `precomputing mcp`, on the 2026-07-28 revision and the older handshake.
- Read and write tokens, a read-only mode, a rate limit and an origin policy for the HTTP server.
- Traces, a store for the model calls of AI agents: each piece kept once by SHA-256, secrets masked, any call rebuilt byte for byte, every call metered.
- Two demos: MCP and Traces.

## 0.1.0, updated (1 October 2026)

- Two demos: Wikipedia, every change to every Wikimedia wiki counted live in the browser, and Incident Déjà Vu, which recognizes an incident that has happened before.
- The log reducer reopens a saved file in the browser. It expected integers where the browser's store hands Go doubles.
- An independent review. The texts now say the Wikipedia demo has run on simulated changes and a local stand-in for the stream, and six small bugs in the two new demos are fixed.

## 0.1.0 (29 September 2026)

- The policy language, the SQL runtime, the Engine, the Meter and Logs, with four demos: SQL, Engine, Meter and Logs.
