"""Times Demo 3's reports through the compiled triggers in native SQLite (Python's sqlite3).

The reports come from `node tools/run-demo3.mjs --csv reports.csv`. The file is kept in WAL
mode with a full sync, and the reports are committed every 10,000, as a gateway would.

    python3 tools/meter-triggers.py build/precomputing reports.csv [out.db]
"""
import csv
import os
import sqlite3
import subprocess
import sys
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    binary, reports = sys.argv[1], sys.argv[2]
    path = sys.argv[3] if len(sys.argv) > 3 else os.path.join(ROOT, "build", "meter-triggers.db")
    for s in ("", "-wal", "-shm"):
        if os.path.exists(path + s):
            os.remove(path + s)
    sql = subprocess.run([binary, "compile", os.path.join(ROOT, "examples", "usage.precompute")],
                         check=True, capture_output=True, text=True).stdout
    db = sqlite3.connect(path, isolation_level=None)
    db.execute("PRAGMA journal_mode=WAL")
    db.execute("PRAGMA synchronous=FULL")
    db.executescript(sql)
    rows = []
    with open(reports) as f:
        for r in csv.reader(f):  # seq, ts, request_id, customer, model, gateway, input_tokens, output_tokens
            rows.append((int(r[1]), r[2], r[3], r[4], r[5], int(r[6]), int(r[7])))
    insert = ("INSERT INTO usage (ts, request_id, customer, model, gateway, input_tokens, output_tokens) "
              "VALUES (?, ?, ?, ?, ?, ?, ?)")
    start = time.perf_counter()
    for i in range(0, len(rows), 10000):
        db.execute("BEGIN")
        db.executemany(insert, rows[i:i + 10000])
        db.execute("COMMIT")
    took = time.perf_counter() - start
    kept = db.execute("SELECT count(*) FROM usage_raw").fetchone()[0]
    refused = db.execute("SELECT coalesce(sum(n), 0) FROM usage_refused").fetchone()[0]
    print(f"SQLite {sqlite3.sqlite_version}: {len(rows)} reports in {took:.2f} s, "
          f"{len(rows) / took:,.0f} a second; {kept} counted, {refused} refused")
    return 0


if __name__ == "__main__":
    sys.exit(main())
