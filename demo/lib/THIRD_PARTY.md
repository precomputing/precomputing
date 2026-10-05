# Third-party code in the demos

| Folder | What it is | License |
|---|---|---|
| `sqlite/` | SQLite WebAssembly 3.53.4 (`@sqlite.org/sqlite-wasm`), unchanged | SQLite is public domain; the JavaScript wrapper is Apache 2.0; the Emscripten glue is MIT / University of Illinois NCSA. The notices are at the top of `index.mjs` |
| `go/wasm_exec.js` | The Go WebAssembly loader from the Go distribution, unchanged | BSD 3-Clause, Copyright The Go Authors |
| `go/precomputing.wasm` | Built from this prototype (`wasm/precomputing`). It includes the Go runtime and standard library | Precomputing: Apache License 2.0. Go: BSD 3-Clause, Copyright The Go Authors |

Everything else in this folder is part of Precomputing, under the Apache License 2.0. Copyright 2026 Precomputing.com.
