"""End-to-end check of compiled policies on native SQLite (Python's sqlite3).

Compiles each example policy with the Go CLI, applies it to a fresh file,
inserts synthetic events and compares every precomputed answer with the
exact value computed from the same events. Exits non-zero on any failure.

    python3 tools/check_native.py path/to/precomputing-binary
"""
import math
import os
import random
import sqlite3
import subprocess
import sys
import tempfile

BIN = sys.argv[1] if len(sys.argv) > 1 else "precomputing"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
failures = []


def check(cond, msg):
    print(("  ok    " if cond else "  FAIL  ") + msg)
    if not cond:
        failures.append(msg)


def compiled(policy, distill=False):
    args = [BIN, "compile"] + (["--distill"] if distill else []) + [policy]
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout


def exact_quantile(xs, q):
    xs = sorted(xs)
    return xs[max(0, math.ceil(q * len(xs)) - 1)]


def latency():
    print("latency.precompute")
    policy = os.path.join(ROOT, "examples", "latency.precompute")
    path = os.path.join(tempfile.mkdtemp(), "latency.db")
    db = sqlite3.connect(path, isolation_level=None)
    schema = compiled(policy)
    db.executescript(schema)
    db.executescript(schema)  # safe to run twice
    check(True, "schema applies twice")
    rnd = random.Random(7)
    eps = {"/api/search": 30, "/api/login": 45, "/api/cart": 80, "/api/checkout": 120, "/api/report": 250}
    names = list(eps)
    t0 = 1790586000
    events, spikes = [], set()
    for i in range(200_000):
        ts = t0 + i * 3 * 3600 // 200_000
        e = names[rnd.randrange(5)]
        v = eps[e] * math.exp(rnd.gauss(0, 0.35))
        if i > 20_000 and i % 997 == 0:
            v = eps[e] * rnd.uniform(8, 20)
            spikes.add(i)
        events.append((ts, e, v))
    db.execute("BEGIN")
    db.executemany("INSERT INTO latency (ts, endpoint, ms) VALUES (?, ?, ?)", events)
    db.execute("COMMIT")
    now = events[-1][0]
    db.execute("BEGIN")
    for stmt in compiled(policy, distill=True).split(";"):
        if "DELETE" in stmt:
            db.execute(stmt, {"now": now})
    db.execute("COMMIT")

    by = {}
    for ts, e, v in events:
        by.setdefault(e, []).append(v)
    got = dict(db.execute("SELECT endpoint, value FROM requests"))
    check(all(got[e] == len(by[e]) for e in by), "requests equal the event counts exactly")
    got = dict(db.execute("SELECT endpoint, value FROM avg_ms"))
    worst = max(abs(got[e] - sum(by[e]) / len(by[e])) / (sum(by[e]) / len(by[e])) for e in by)
    check(worst < 1e-12, f"avg_ms exact (worst relative difference {worst:.1e})")
    got = dict(db.execute("SELECT endpoint, value FROM p99_ms"))
    worst = max(abs(got[e] - exact_quantile(by[e], 0.99)) / exact_quantile(by[e], 0.99) for e in by)
    check(worst <= 0.01 + 1e-9, f"p99_ms within 1% of exact (worst {worst * 100:.3f}%)")
    n1h = db.execute("SELECT sum(n) FROM latency_win WHERE res = 3600").fetchone()[0]
    check(n1h == len(events), "1-hour windows count every event once")
    n1m = db.execute("SELECT sum(n) FROM latency_win WHERE res = 60").fetchone()[0]
    check(n1m == len(events), "1-minute windows count every event once")
    mins = db.execute("SELECT min(ms_min), max(ms_max) FROM latency_win WHERE res = 3600").fetchone()
    check(abs(mins[0] - min(v for _, _, v in events)) < 1e-9 and abs(mins[1] - max(v for _, _, v in events)) < 1e-9, "minimum and maximum exact")
    kept = {(ts, e, v) for ts, e, v in db.execute("SELECT ts, endpoint, ms FROM latency_anomaly")}
    found = sum(1 for i in spikes if events[i] in kept)
    check(found >= 0.95 * len(spikes), f"anomalies kept {found} of {len(spikes)} planted spikes")
    over = db.execute("SELECT count(*) FROM (SELECT count(*) c FROM latency_anomaly GROUP BY endpoint, ts / 60 HAVING c > 20)").fetchone()[0]
    check(over == 0, "no minute keeps more than 20 anomalies per endpoint")
    per_min = db.execute("SELECT max(c) FROM (SELECT count(*) c FROM latency_sample GROUP BY res, w, endpoint)").fetchone()[0]
    check(per_min == 3, "three samples per endpoint per minute")
    raw_left = db.execute("SELECT min(ts) FROM latency_raw").fetchone()[0]
    check(raw_left >= now - 300, "distill kept only the last 5 minutes of raw events")
    first = db.execute("SELECT ms_first FROM latency_win WHERE res = 3600 AND endpoint = '/api/search' ORDER BY w LIMIT 1").fetchone()[0]
    check(first == next(v for ts, e, v in events if e == "/api/search"), "first value of the first hour is the first event")
    try:
        db.execute("INSERT INTO latency (ts, endpoint, ms) VALUES (?, ?, NULL)", (now, "/api/search"))
        check(False, "a null value is refused")
    except sqlite3.IntegrityError as e:
        check("must not be null" in str(e), "a null value is refused with a clear message")
    meta = dict(db.execute("SELECT key, value FROM _precomputing"))
    check(meta["format"] == "1" and "stream latency" in meta["policy"], "the file carries its format and policy")
    db.close()


def trades():
    print("trades.precompute")
    policy = os.path.join(ROOT, "examples", "trades.precompute")
    db = sqlite3.connect(":memory:", isolation_level=None)
    db.executescript(compiled(policy))
    rnd = random.Random(3)
    t0 = 1790586000
    rows = []
    price = {"AAA": 100.0, "BBB": 20.0}
    jump = 30_001  # one AAA trade 8% above the one before it; the price stays up
    for i in range(50_000):
        sym = "AAA" if i % 3 else "BBB"
        price[sym] *= math.exp(rnd.gauss(0, 0.0005))
        if i == jump:
            price[sym] *= 1.08
        rows.append((t0 + i // 10, sym, round(price[sym], 4), rnd.randint(1, 500)))
    db.execute("BEGIN")
    db.executemany("INSERT INTO trades (ts, symbol, price, size) VALUES (?, ?, ?, ?)", rows)
    db.execute("COMMIT")
    # One 1-minute candle checked against the raw trades.
    w = (t0 // 60 + 5) * 60
    sel = [r for r in rows if r[1] == "AAA" and w <= r[0] < w + 60]
    o, h, l, c, v, nv = db.execute("SELECT price_first, price_max, price_min, price_last, size_sum, notional_sum FROM trades_win WHERE res = 60 AND w = ? AND symbol = 'AAA'", (w,)).fetchone()
    check((o, h, l, c) == (sel[0][2], max(r[2] for r in sel), min(r[2] for r in sel), sel[-1][2]), "a 1-minute candle matches open, high, low and close")
    vwap = nv / v
    exact = sum(r[2] * r[3] for r in sel) / sum(r[3] for r in sel)
    check(abs(vwap - exact) < 1e-9, "VWAP from the derived notional is exact")
    last = dict(db.execute("SELECT symbol, value FROM last_price"))
    check(last["AAA"] == [r for r in rows if r[1] == "AAA"][-1][2], "last_price is the last trade")
    day = db.execute("SELECT period, value FROM day_volume WHERE symbol = 'BBB'").fetchall()
    check(len(day) == 1 and day[0][1] == sum(r[3] for r in rows if r[1] == "BBB"), "volume per day sums every trade")
    bbb = [r for r in rows if r[1] == "BBB"]
    board = db.execute("""SELECT o.value, h.value, l.value, t.value FROM day_open o
        JOIN day_high h USING (symbol, period) JOIN day_low l USING (symbol, period)
        JOIN day_turnover t USING (symbol, period) WHERE symbol = 'BBB'""").fetchone()
    want = (bbb[0][2], max(r[2] for r in bbb), min(r[2] for r in bbb))
    check(board[:3] == want and abs(board[3] - sum(r[2] * r[3] for r in bbb)) < 1e-6 * board[3], "day open, high, low and turnover match the trades")
    flagged = db.execute("SELECT ts, symbol, price FROM trades_anomaly").fetchall()
    check(flagged == [rows[jump][:3]], f"the price jump is the only step flagged ({len(flagged)} flagged)")
    an = db.execute("SELECT sum(an) FROM trades_win WHERE res = 60").fetchone()[0]
    check(an == 1, "after the jump the next steps are judged from the new level")
    total = db.execute("SELECT value FROM trades_total").fetchone()[0]
    check(total == len(rows), "a precompute with no keys counts every event")


def usage():
    print("usage.precompute")
    policy = os.path.join(ROOT, "examples", "usage.precompute")
    db = sqlite3.connect(":memory:", isolation_level=None)
    db.executescript(compiled(policy))
    sep1 = 1788220800  # 2026-09-01 00:00 UTC
    oct1 = 1790812800  # 2026-10-01 00:00 UTC
    ins = "INSERT INTO usage (ts, request_id, customer, model, gateway, input_tokens, output_tokens) VALUES (?, ?, ?, ?, ?, ?, ?)"
    rnd = random.Random(5)
    sent = []
    for i in range(20000):
        ts = sep1 + i * 120
        ev = (ts, f"r{i}", rnd.choice(["acme", "birch"]), rnd.choice(["small", "large"]), rnd.choice(["eu", "us"]), rnd.randint(10, 900), rnd.randint(10, 400))
        sent.append(ev)
    db.execute("BEGIN")
    repeats = 0
    for i, ev in enumerate(sent):
        db.execute(ins, ev)
        if i % 400 == 399:                   # a retried request: the same id again, a little later
            db.execute(ins, sent[i - 3])
            repeats += 1
    newest = sent[-1][0]
    late_ok = (newest - 3600, "late-ok", "acme", "small", "eu", 100, 50)     # an hour late: counted
    late_no = (newest - 180000, "late-no", "acme", "small", "eu", 100, 50)   # 50 hours late: refused
    db.execute(ins, late_ok)
    db.execute(ins, late_no)
    db.execute("COMMIT")
    kept = sent + [late_ok]
    refused = dict(db.execute("SELECT reason, sum(n) FROM usage_refused GROUP BY reason"))
    check(refused.get("repeat") == repeats, f"every retried request refused ({refused.get('repeat')} of {repeats})")
    check(refused.get("late") == 1, "a request 50 hours late is refused, one an hour late is counted")
    tok = dict(db.execute("SELECT customer, value FROM tokens_month WHERE period = '2026-09'"))
    want = {}
    for ev in kept:
        want[ev[2]] = want.get(ev[2], 0) + ev[5] + ev[6]
    check(tok == want, "tokens per customer for September are exact")
    rows = db.execute("SELECT count(*) FROM usage_raw").fetchone()[0]
    check(rows == len(kept), "every request is kept whole while September is open")
    # The month closes a day after it ends: a September request arriving on October 2 is refused.
    db.execute(ins, (oct1 + 86400 + 60, "oct-2", "acme", "small", "eu", 1, 1))
    db.execute(ins, (oct1 - 60, "sep-30-late", "acme", "small", "eu", 1, 1))
    closed = db.execute("SELECT sum(n) FROM usage_refused WHERE reason IN ('closed', 'late')").fetchone()[0]
    check(closed == 2 and dict(db.execute("SELECT customer, value FROM tokens_month WHERE period = '2026-09'")) == want,
          "after the month closes its totals never change")
    db.execute("INSERT INTO monthly_tokens_limit (customer, lim) VALUES ('acme', ?)", (want["acme"] - 1,))
    q = db.execute("SELECT used, lim, remaining, reached FROM monthly_tokens WHERE customer = 'acme' AND period = '2026-09'").fetchone()
    check(q == (want["acme"], want["acme"] - 1, -1, 1), "the quota view shows use against the limit")
    distill = [l for l in compiled(policy, True).splitlines() if l.startswith("DELETE")]
    for sql in distill:
        db.execute(sql, {"now": oct1 + 86400 + 60})
    check(db.execute("SELECT count(*) FROM usage_raw WHERE ts < ?", (oct1,)).fetchone()[0] == len(kept), "distill keeps September's requests during the dispute window")
    for sql in distill:
        db.execute(sql, {"now": oct1 + 86400 + 91 * 86400})
    check(db.execute("SELECT count(*) FROM usage_raw WHERE ts < ?", (oct1,)).fetchone()[0] == 0, "90 days after the close, September's requests are distilled")
    check(dict(db.execute("SELECT customer, value FROM tokens_month WHERE period = '2026-09'")) == want, "and the invoice totals stay the same")


latency()
trades()
usage()
print("\n" + ("all checks passed" if not failures else f"{len(failures)} check(s) failed"))
sys.exit(1 if failures else 0)
