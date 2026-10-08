#!/bin/sh
# Copyright 2026 Precomputing.com
# SPDX-License-Identifier: Apache-2.0
#
# The check every release binary has to pass before it goes out: compiles a
# policy, keeps a file current from events through the Engine, reads the
# answers back, then runs Demo 2's whole trading day and its recount, which
# fails on a single difference.
#
#   sh tools/smoke.sh ./precomputing
set -eu
bin=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

cat > "$dir/latency.precompute" <<'EOF'
stream latency {
  key   endpoint text
  value ms real
  raw     keep 5m
  rollup  1m keep 30d quantiles ms
}
precompute requests = count(latency) by endpoint
precompute avg_ms   = avg(latency.ms) by endpoint
EOF

"$bin" version
"$bin" compile "$dir/latency.precompute" | grep -q 'CREATE TRIGGER'

printf '1790586000,/api/search,30\n1790586001,/api/search,40\n1790586002,/api/cart,12\n' |
  "$bin" put --policy "$dir/latency.precompute" "$dir/latency.db" > "$dir/put.log"
grep -q '^ok ' "$dir/put.log"

"$bin" get "$dir/latency.db" "SELECT value FROM requests WHERE endpoint = '/api/search'" | grep -qx ' *2 *'
"$bin" get "$dir/latency.db" "SELECT value FROM avg_ms WHERE endpoint = '/api/search'" | grep -qx ' *35\(\.0\)\? *'
"$bin" inspect "$dir/latency.db" > /dev/null

"$bin" demo --db "$dir/demo.db"

echo "smoke test passed"
