"""Shows that comparing the Engine with the compiled triggers is a real check.

Each mutant is a copy of the code with one small change to the Engine's rules. The
equivalence test must pass on the unchanged copy and fail on every mutant.

    python3 tools/mutants.py
"""
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TEST = ["go", "test", "-count=1", "-run", "TestEngineMatchesSQL", "./engine/sqlitestore/"]

MUTANTS = [
    ("the last value of a window stays put on a tie",
     "engine/stream.go", "if ts >= w.lastTS {\n\t\t\tst[5] = x", "if ts > w.lastTS {\n\t\t\tst[5] = x"),
    ("the first value of a window moves on a tie",
     "engine/stream.go", "if ts < w.firstTS {\n\t\t\tst[4] = x", "if ts <= w.firstTS {\n\t\t\tst[4] = x"),
    ("the baseline's weighting starts one event late",
     "engine/stream.go", "if b.n < a.warm {", "if b.n <= a.warm {"),
]


def copy():
    dst = tempfile.mkdtemp(prefix="precomputing-mutant-")
    shutil.rmtree(dst)
    shutil.copytree(ROOT, dst, ignore=shutil.ignore_patterns("build", "node_modules", "demo", ".git"))
    return dst


def run(dir):
    p = subprocess.run(TEST, cwd=dir, capture_output=True, text=True)
    return p.returncode == 0, p.stdout + p.stderr


def main():
    failures = 0
    d = copy()
    ok, out = run(d)
    shutil.rmtree(d)
    print(("  ok    " if ok else "  FAIL  ") + "the unchanged code passes the equivalence test")
    if not ok:
        print(out)
        return 1
    for name, path, old, new in MUTANTS:
        d = copy()
        f = os.path.join(d, path)
        src = open(f).read()
        if src.count(old) != 1:
            print(f"  FAIL  cannot apply the mutant: {name}")
            failures += 1
            shutil.rmtree(d)
            continue
        open(f, "w").write(src.replace(old, new))
        passed, out = run(d)
        shutil.rmtree(d)
        caught = not passed
        print(("  ok    " if caught else "  FAIL  ") + f"the test fails when {name}")
        failures += 0 if caught else 1
    print("\nall mutants caught" if failures == 0 else f"\n{failures} mutants not caught")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
