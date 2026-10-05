#!/bin/sh
# The full verification run: every check and every published number, one after another.
# Needs the demos built (tools/build-demos.sh, after `cd tools && npm install`) and, for the
# Drain check, the Loghub samples (sh tools/fetch-loghub.sh). Demo 8's day comes with the prototype
# (demo/traces/app/data); tools/traces-generate.mjs and tools/traces-prepare.mjs make it again.
# Outputs go to build/verify.
# Speeds depend on the machine and on what else runs on it; counts and checks do not.
cd "$(dirname "$0")/.."
V=build/verify
mkdir -p $V
go build -trimpath -o build/precomputing ./cmd/precomputing && go build -trimpath -o build/filecompare ./tools/filecompare || exit 1
# t runs a command and reports its time and exit status on standard error, so that output sent to a
# file stays clean.
t() { s=$(date +%s.%N); "$@"; rc=$?; e=$(date +%s.%N); echo "[$(awk "BEGIN { printf \"%.1f\", $e - $s }") s, exit $rc] $*" >&2; }
echo "== environment"; uname -sr; nproc; grep -m1 "model name" /proc/cpuinfo; go version; node --version; build/precomputing version
echo "== go test"; t go test -count=1 ./...
if [ -d build/loghub ]; then echo "== loghub"; LOGHUB=build/loghub t go test -count=1 -run TestLoghub -v ./internal/drain 2>&1 | tail -20; fi
echo "== check_native"; t python3 tools/check_native.py build/precomputing 2>&1 | tail -3
echo "== mutants"; t python3 tools/mutants.py
echo "== logarithms"; t sh -c "go run ./tools/logbits | node tools/logbits.mjs"
echo "== demo1"; t node tools/run-demo1.mjs --json --db $V/latency.sqlite > $V/demo1.json
echo "== demo2"; t node tools/run-demo2.mjs --json --db $V/trades.sqlite > $V/demo2.json
echo "== demo3"; t node tools/run-demo3.mjs --json --csv $V/reports.csv --db $V/usage-sql.db > $V/demo3.json
echo "== demo3 native"; rm -f $V/usage-engine.db*; t sh -c "build/precomputing put --policy examples/usage.precompute --seq $V/usage-engine.db < $V/reports.csv > $V/put3.log 2>&1"; tail -1 $V/put3.log
echo "== demo3 compare"; t sh -c "build/filecompare -skip customers,model_costs,plans,prices,monthly_tokens_limit $V/usage-engine.db $V/usage-sql.db | tail -1"
echo "== demo3 triggers in native SQLite"; t python3 tools/meter-triggers.py build/precomputing $V/reports.csv $V/usage-triggers.db
echo "== demo4"; t node tools/run-demo4.mjs --json --lines $V/shop.log --db $V/shop-wasm.db > $V/demo4.json
echo "== demo4 native"; rm -f $V/shop-native.db*; t sh -c "build/precomputing put --lines --policy examples/shop.precompute $V/shop-native.db < $V/shop.log > $V/put4.log 2>&1"; tail -1 $V/put4.log
echo "== demo4 compare"; t sh -c "build/filecompare $V/shop-native.db $V/shop-wasm.db | tail -1"
echo "== demo5"; t node tools/run-demo5.mjs --json --lines $V/wiki.jsonl --db $V/wiki-wasm.db > $V/demo5.json
echo "== demo5 native"; rm -f $V/wiki-native.db*; t sh -c "build/precomputing put --format json --policy examples/wikipedia.precompute $V/wiki-native.db < $V/wiki.jsonl > $V/put5.log 2>&1"; tail -1 $V/put5.log
echo "== demo5 compare"; t sh -c "build/filecompare $V/wiki-native.db $V/wiki-wasm.db | tail -1"
echo "== demo6"; t node tools/run-demo6.mjs --json --history --trials 20 --lines $V/dejavu.log --db $V/dejavu-wasm.db > $V/demo6.json
echo "== demo6 native"; rm -f $V/dejavu-native.db*; t sh -c "build/precomputing put --lines --policy examples/dejavu.precompute $V/dejavu-native.db < $V/dejavu.log > $V/put6.log 2>&1"; tail -1 $V/put6.log
echo "== demo6 compare"; t sh -c "build/filecompare -skip incidents,incident_keys $V/dejavu-native.db $V/dejavu-wasm.db | tail -1"
echo "== demo7 files"; cp $V/usage-sql.db $V/usage.sqlite; cp $V/shop-wasm.db $V/shop.sqlite; t node tools/mcp-files.mjs $V
echo "== demo7"; t node tools/run-demo7.mjs --json --native build/precomputing > $V/demo7.json; tail -c 300 $V/demo7.json
echo "== mcp clients"; for f in latency trades usage shop; do t node tools/mcp-check.mjs build/precomputing $V/$f.sqlite --json > $V/mcp-check-$f.json; done
echo "== demo8"; t node tools/run-demo8.mjs --json --db $V/traces-wasm.db --native build/precomputing > $V/demo8.json; tail -c 300 $V/demo8.json
echo "== demo8 native"; for i in 1 2 3; do rm -f $V/traces-native.db*; t sh -c "build/precomputing traces --policy examples/traces.precompute --again generated-0066 $V/traces-native.db < demo/traces/app/data/day.jsonl.gz > $V/traces-native.log"; done; cat $V/traces-native.log
echo "== demo8 rebuild"; build/precomputing traces --rebuild 'generated-0046#67' $V/traces-native.db | sha256sum
echo "== native demo"; rm -f $V/trades.db*; t build/precomputing demo --db $V/trades.db --json > $V/demo2-native.json
echo "== crash lab"; t go run ./tools/crashlab -bin build/precomputing -kills 100 > $V/crashlab.log 2>&1; tail -2 $V/crashlab.log
echo "== done"
