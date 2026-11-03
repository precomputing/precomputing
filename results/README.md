# Results for 0.3.0

The checks run on 3 November 2026 for this release, with Go 1.24.7 on linux/amd64 (a two-core cloud server, Intel Xeon at 2.1 GHz). CI runs the same tests on the current stable Go on Linux (x86-64 and ARM64) and macOS.

| File | What it holds |
|---|---|
| `go-test.log` | `go vet`, `go test` for every package and the race detector, on the commit that raised the version |
| `check-0.3.log` | `tools/check-0.3.sh` on the 0.3.0 binary: HTTPS, OTLP logs and metrics in both encodings, a retried request, a read token, a request of 15 million empty metrics, `ahead` and `migrate` |
| `demos.log` | `tools/verify.sh`: the native checks, the mutants, the logarithms, the eight demos run headless in Node with each demo's own code, the native runs and the native-against-browser comparisons, with times |
| `demo1.json` to `demo8.json`, `demo2-native.json` | Each headless run's figures, the ones the site and the README publish |
| `mcp-check-*.json` | Both official MCP clients against the native server, on each of the four demo files |
| `put3.log` to `put6.log`, `traces-native.log` | The native runs on the same data |

`demos.log` was run on commit b927d60, with every 0.3 feature in. The commits after it, the review's fixes, change only the OTLP intake, `migrate`, the TLS code and `serve`, which the demos don't use. Every comparison came out as in 0.2: 0 differences for Demos 3, 5 and 6, and for Demo 4 the one value out of 1,794,042 whose last bit depends on the platform's logarithm, as the documentation says.
