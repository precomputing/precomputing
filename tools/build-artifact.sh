#!/bin/sh
# Builds a standalone copy of one demo for a hosted preview: the same files as the site, with
# the page as a fragment (the host adds the document skeleton) and site-only parts switched off.
# Usage: tools/build-artifact.sh sql|engine|meter|logs|live|dejavu|mcp|traces   (writes build/artifact-DEMO,
# plus wrapped.html, a whole page around the fragment for local testing)
set -eu
cd "$(dirname "$0")/.."
DEMO=${1:-sql}
OUT=build/artifact-$DEMO
rm -rf "$OUT" && mkdir -p "$OUT/demo/$DEMO/app" "$OUT/demo/lib/sqlite" "$OUT/demo/lib/go"
cp demo/lib/kit.css demo/lib/kit.js demo/lib/engine.js "$OUT/demo/lib/"
cp demo/lib/sqlite/index.mjs demo/lib/sqlite/sqlite3.wasm demo/lib/sqlite/sqlite3-opfs-async-proxy.js "$OUT/demo/lib/sqlite/"
cp demo/lib/go/precomputing.wasm demo/lib/go/wasm_exec.js "$OUT/demo/lib/go/"
for f in demo/$DEMO/app/*; do
  case "$f" in *.html) ;; *) cp -r "$f" "$OUT/demo/$DEMO/app/" ;; esac
done
# A hosted preview serves no gzip files: Demo 6's day before goes as base64 text (worker.js reads it).
if [ -f "$OUT/demo/$DEMO/app/history.db.gz" ]; then
  base64 -w0 "$OUT/demo/$DEMO/app/history.db.gz" > "$OUT/demo/$DEMO/app/history.b64.txt"
  rm "$OUT/demo/$DEMO/app/history.db.gz"
fi
python3 - "$OUT" "$DEMO" <<'PY'
import re, sys
out, demo = sys.argv[1], sys.argv[2]
page = open(f"demo/{demo}/app/index.html").read()
title = re.search(r"<title>.*?</title>", page).group(0)
desc = re.search(r'<meta name="description"[^>]*>', page).group(0)
body = page[page.index("<body>") + len("<body>"):page.index("</body>")]
body = body.replace('<script type="module" src="app.js"></script>',
    f'<script>window.PRECOMPUTING_HOST = "artifact";</script>\n<script type="module" src="demo/{demo}/app/app.js"></script>')
frag = "\n".join([title, desc, '<link rel="stylesheet" href="demo/lib/kit.css">', body.strip(), ""])
open(f"{out}/index.html", "w").write(frag)
head = "\n".join([title, desc, '<link rel="stylesheet" href="demo/lib/kit.css">'])
open(f"{out}/wrapped.html", "w").write('<!doctype html>\n<html lang="en"><head><meta charset="utf-8">\n'
    '<meta name="viewport" content="width=device-width, initial-scale=1">\n' + head + "\n</head><body>\n" + body.strip() + "\n</body></html>\n")
PY
echo "artifact built in $OUT"
