#!/bin/sh
# End-to-end checks of what 0.3 added, on a built binary: HTTPS, OpenTelemetry over OTLP/HTTP,
# a request of empty messages, ahead, and migrate. Needs curl, openssl and python3.
#   sh tools/check-0.3.sh build/precomputing
set -u
BIN=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
ROOT=$(cd "$(dirname "$0")/.." && pwd)
T=$ROOT/otlp/testdata
W=$(mktemp -d)
cd "$W"
fails=0
check() { # what, got, want
  if [ "$2" = "$3" ]; then echo "ok    $1: $2"; else echo "FAIL  $1: got $2, want $3"; fails=$((fails + 1)); fi
}
"$BIN" version

echo "== HTTPS and OTLP"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout key.pem -out cert.pem -days 1 \
  -subj /CN=localhost -addext subjectAltName=DNS:localhost 2>/dev/null
printf 'read  rrrrrrrrrrrrrrrrrrrr agent\nwrite wwwwwwwwwwwwwwwwwwww gateway\n' > tokens
cat > otel.precompute <<'EOF'
logs {}
stream errors from logs where level = "ERROR" {
  key    service text
  value  ms real
  rollup 1m keep 1d
}
stream requests from metrics "http.requests" {
  key    http_route text
  value  calls integer
  rollup 1m keep 1d
}
precompute errors_by_service = count(errors) by service
precompute requests_total = sum(requests.calls) by http_route
EOF
"$BIN" serve --policy otel.precompute --addr localhost:18471 --token-file tokens --tls-cert cert.pem --tls-key key.pem o.db 2> serve.log &
PID=$!
sleep 1
U=https://localhost:18471
C="curl -s --cacert cert.pem -H Authorization:Bearer\ wwwwwwwwwwwwwwwwwwww"
code() { curl -s -o /dev/null -w "%{http_code}" --cacert cert.pem "$@"; }
WT="Authorization: Bearer wwwwwwwwwwwwwwwwwwww"
check "logs, protobuf, over HTTPS" "$(code -H "$WT" -H Content-Type:application/x-protobuf --data-binary @$T/logs.pb $U/v1/logs)" 200
gzip -c "$T/metrics-1.json" > m1.gz
check "metrics, gzipped JSON" "$(code -H "$WT" -H Content-Type:application/json -H Content-Encoding:gzip --data-binary @m1.gz $U/v1/metrics)" 200
check "metrics, protobuf" "$(code -H "$WT" -H Content-Type:application/x-protobuf --data-binary @$T/metrics-2.pb $U/v1/metrics)" 200
check "the same request again" "$(code -H "$WT" -H Content-Type:application/x-protobuf --data-binary @$T/metrics-2.pb $U/v1/metrics)" 200
check "a read token posting metrics" "$(code -H 'Authorization: Bearer rrrrrrrrrrrrrrrrrrrr' -H Content-Type:application/x-protobuf --data-binary @$T/metrics-2.pb $U/v1/metrics)" 403
check "text/plain" "$(code -H "$WT" -H Content-Type:text/plain --data-binary @$T/metrics-2.pb $U/v1/metrics)" 415
check "plain HTTP to the HTTPS port" "$(curl -s -o /dev/null -w "%{http_code}" http://localhost:18471/)" 400
total=$(curl -s --cacert cert.pem -H "$WT" $U/v1/answers/requests_total | python3 -c 'import json,sys; print(sum(r["value"] for r in json.load(sys.stdin)))')
check "requests counted: the first cumulative point a baseline, the repeat once" "$total" 15
errs=$(curl -s --cacert cert.pem -H "$WT" $U/v1/answers/errors_by_service | python3 -c 'import json,sys; print(" ".join("%s=%s" % (r["service"], r["value"]) for r in json.load(sys.stdin)))')
check "errors by service from log records" "$errs" "checkout=1 web=1"
python3 - <<'EOF'
import gzip
def var(x):
    o = b''
    while x >= 0x80:
        o += bytes([x & 0x7f | 0x80]); x >>= 7
    return o + bytes([x])
inner = b'\x12\x00' * 15_000_000
sm = b'\x12' + var(len(inner)) + inner
open('empty.gz', 'wb').write(gzip.compress(b'\x0a' + var(len(sm)) + sm))
EOF
echo "a request of 15 million empty metrics, $(wc -c < empty.gz | tr -d ' ') bytes gzipped:"
check "  refused" "$(code -H "$WT" -H Content-Type:application/x-protobuf -H Content-Encoding:gzip --data-binary @empty.gz $U/v1/metrics)" 400
rss=$(ps -o rss= -p $PID | tr -d ' ')
check "  server memory under 256 MB after it" "$([ "$rss" -lt 262144 ] && echo yes || echo "no, $rss kB")" yes
kill -INT $PID
wait $PID
tail -1 serve.log

echo "== ahead and migrate"
echo "1790290800,r1,harbor,large,eu,2410,880" | "$BIN" put --policy "$ROOT/examples/usage.precompute" u.db > /dev/null 2>&1
sed 's/late   48h /late   48h\n  ahead  1h /' "$ROOT/examples/usage.precompute" |
  sed 's/^precompute tokens_month/precompute calls_day      = count(usage) by customer per day\nprecompute tokens_month/' > u2.precompute
"$BIN" migrate --policy u2.precompute --dry-run u.db
"$BIN" migrate --policy u2.precompute u.db
sed 's/period month close 24h/period day close 24h/' u2.precompute > u3.precompute
"$BIN" migrate --policy u3.precompute u.db
check "a period change refused" "$?" 1
printf '1790294400,r2,harbor,large,eu,10,10\n4102444800,r3,harbor,large,eu,10,10\n' | "$BIN" put u.db > /dev/null 2>&1
check "calls_day after the migration" "$("$BIN" get u.db 'SELECT sum(value) FROM calls_day' | tail -1 | tr -d ' ')" 1
check "a report stamped in 2100" "$("$BIN" get u.db "SELECT reason || ' ' || n FROM usage_refused" | tail -1)" "ahead 1"
"$BIN" inspect u.db | sed -n '/^Migrations:/,$p'
rm -rf "$W"
echo "$fails failed"
[ "$fails" = 0 ]
