Precomputing keeps answers ready as data arrives. A short policy names the streams of events, the answers you want ready and how long each level of detail should live, and one SQLite file keeps those answers current. Any SQLite tool reads the file.

0.1.1-alpha is the first public release, under the Apache License 2.0. These binaries run on Linux only for now. The code has been checked on simulated and generated data, nobody has run it on production traffic yet, and it hasn't had an outside security review, so don't put it in front of anything that matters.

## What's in it

- The policy language and its compiler to plain SQLite: tables, views and one trigger per stream.
- The Engine: the same policy run in memory, writing the same file at every checkpoint, 12 to 17 times faster than the triggers.
- The Meter for exact usage billing, Logs for log dashboards, and Traces for the model calls of AI agents.
- An MCP server, so AI agents can read a file's answers, and an HTTP server with read and write tokens.
- Eight live demos at https://precomputing.com/demo/, all on the same code. The repository's README lists what each one measured.

## Install

Pick the tarball for your CPU: `precomputing-0.1.1-alpha-linux-amd64.tar.gz` for x86-64, or `precomputing-0.1.1-alpha-linux-arm64.tar.gz` for 64-bit ARM. Check it against `SHA256SUMS` before you unpack it:

```sh
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf precomputing-0.1.1-alpha-linux-amd64.tar.gz
./precomputing-0.1.1-alpha-linux-amd64/precomputing version
```

Each tarball holds the `precomputing` binary with `LICENSE`, `NOTICE`, `THIRD-PARTY-LICENSES.txt` and `README.md`. `precomputing demo` runs Demo 2's whole trading day on your machine and checks it.

## How they were built

Go 1.24.7 with SQLite 3.53.4 compiled in through cgo, linked statically against musl with zig cc 0.16.0, and stripped:

```sh
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC="zig cc -target x86_64-linux-musl" \
  go build -trimpath -ldflags='-s -w -linkmode external -extldflags "-static -s"' ./cmd/precomputing
```

The arm64 binary is built the same way with `GOARCH=arm64` and `-target aarch64-linux-musl`.

The amd64 binary was run before release. `precomputing demo` took in Demo 2's 4,048,210 trades and found 0 differences in its recount, and on the data of Demos 3 to 8 it wrote the same files as the development build, value for value. The arm64 binary was built the same way but hasn't been run yet.
