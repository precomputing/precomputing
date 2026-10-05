#!/usr/bin/env python3
"""Reads agent runs out of a Parquet file of nebius/SWE-rebench-openhands-trajectories and writes
them as JSON lines, one run each, for tools/traces-prepare.mjs:

    python3 tools/traces-from-parquet.py FILE.parquet --sample 100 > build/traces/nebius.jsonl
    node tools/traces-prepare.mjs build/traces/nebius.jsonl --source nebius

The dataset is by Nebius, under CC BY 4.0 (https://creativecommons.org/licenses/by/4.0/): a page
or file that shows its runs must credit it, and traces-prepare.mjs writes the credit line into
meta.json when --source is nebius. Needs pyarrow (pip install pyarrow).

It reads these columns by name, in any order: trajectory_id, instance_id, repo, trajectory (the
messages in the OpenAI chat format, as a list or as JSON text), tools (likewise), exit_status and
resolved. A missing column is left out, except trajectory, which every run needs.

--sample N keeps N runs spread evenly over the file, the same ones every time.
--max-messages M leaves out runs with more messages than that.
"""
import argparse
import json
import sys

COLUMNS = ["trajectory_id", "instance_id", "repo", "trajectory", "tools", "exit_status", "resolved"]
MESSAGE_KEYS = ["role", "content", "tool_calls", "tool_call_id", "name"]


def as_list(v):
    """A list column, whether Parquet holds it as a list or as JSON text."""
    if v is None:
        return []
    if isinstance(v, (str, bytes)):
        v = json.loads(v)
    return list(v)


def clean(v):
    """Drops the None fields a Parquet struct gives every row, all the way down."""
    if isinstance(v, dict):
        return {k: clean(x) for k, x in v.items() if x is not None}
    if isinstance(v, list):
        return [clean(x) for x in v]
    return v


def message(m):
    if isinstance(m, (str, bytes)):
        m = json.loads(m)
    m = clean(dict(m))
    out = {k: m[k] for k in MESSAGE_KEYS if k in m}
    if isinstance(out.get("tool_calls"), (str, bytes)):
        out["tool_calls"] = json.loads(out["tool_calls"])
    if "role" not in out:
        raise ValueError("a message has no role")
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("parquet")
    ap.add_argument("--sample", type=int, default=0, help="runs to keep, spread evenly over the file")
    ap.add_argument("--max-messages", type=int, default=0, help="leave out runs with more messages")
    a = ap.parse_args()
    try:
        import pyarrow.parquet as pq
    except ImportError:
        sys.exit("traces-from-parquet: needs pyarrow (pip install pyarrow)")

    pf = pq.ParquetFile(a.parquet)
    names = set(pf.schema_arrow.names)
    if "trajectory" not in names:
        sys.exit(f"traces-from-parquet: {a.parquet} has no trajectory column; it has {sorted(names)}")
    cols = [c for c in COLUMNS if c in names]
    total = pf.metadata.num_rows
    want = None
    if a.sample and a.sample < total:
        step = total / a.sample
        want = {int(i * step) for i in range(a.sample)}

    kept = skipped = 0
    row = 0
    out = sys.stdout
    for batch in pf.iter_batches(columns=cols, batch_size=256):
        n = batch.num_rows
        picks = [i for i in range(n) if want is None or row + i in want]
        row += n
        if not picks:
            continue
        for r in batch.take(picks).to_pylist():
            try:
                traj = [message(m) for m in as_list(r.get("trajectory"))]
            except (ValueError, TypeError, json.JSONDecodeError) as e:
                print(f"traces-from-parquet: run {r.get('trajectory_id')}: {e}; left out", file=sys.stderr)
                skipped += 1
                continue
            if not traj or (a.max_messages and len(traj) > a.max_messages):
                skipped += 1
                continue
            run = {
                "trajectory_id": str(r.get("trajectory_id") or f"run-{kept + skipped + 1}"),
                "instance_id": str(r.get("instance_id") or ""),
                "repo": str(r.get("repo") or "unknown"),
                "trajectory": traj,
                "tools": clean(as_list(r.get("tools"))),
                "exit_status": str(r.get("exit_status") or ""),
                "resolved": bool(r.get("resolved")) if r.get("resolved") is not None else False,
            }
            out.write(json.dumps(run, ensure_ascii=False) + "\n")
            kept += 1
    print(f"traces-from-parquet: {kept} runs written, {skipped} left out, of {total} in the file", file=sys.stderr)


if __name__ == "__main__":
    main()
