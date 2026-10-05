#!/bin/sh
# Builds the demo files from the sources: compiles each demo's policy with the real compiler,
# builds the compiler and the Engine for the browser (one file), and copies SQLite WebAssembly.
set -eu
cd "$(dirname "$0")/.."
BIN=build/precomputing
go build -trimpath -o "$BIN" ./cmd/precomputing
mkdir -p demo/lib/go
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o demo/lib/go/precomputing.wasm ./wasm/precomputing
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" demo/lib/go/wasm_exec.js
rm -rf demo/lib/compiler
compile_demo() { # policy, app dir
  cp "examples/$1.precompute" "demo/$2/app/policy.precompute"
  "$BIN" compile -o "demo/$2/app/policy.sql" "examples/$1.precompute"
  "$BIN" compile --distill -o "demo/$2/app/policy.distill.sql" "examples/$1.precompute"
}
compile_demo latency sql
compile_demo usage meter
# Demo 4 compiles in the page too; its policy is made from its dashboard by the importer.
"$BIN" import examples/shop-dashboard.json > examples/shop.precompute
cp examples/shop.precompute examples/shop-dashboard.json demo/logs/app/
# Demo 2 compiles its policy in the page, with the same Go program that runs the Engine; so does
# Demo 8, whose day of agent runs tools/traces-prepare.mjs writes.
cp examples/trades.precompute demo/engine/app/policy.precompute
# Demo 5 compiles its policy in the page too.
cp examples/wikipedia.precompute demo/live/app/policy.precompute
# Demo 6 starts from a file that holds the shop's last 24 hours, made here by the Engine above.
(cd tools && node build-dejavu-history.mjs)
cp examples/traces.precompute demo/traces/app/policy.precompute
SQLITE=tools/node_modules/@sqlite.org/sqlite-wasm/dist
mkdir -p demo/lib/sqlite
cp "$SQLITE/index.mjs" "$SQLITE/sqlite3.wasm" "$SQLITE/sqlite3-opfs-async-proxy.js" demo/lib/sqlite/
cp tools/node_modules/@sqlite.org/sqlite-wasm/package.json demo/lib/sqlite/package.json
echo "demos built"
