# Results for 0.1.1-alpha

The checks run on 5 October 2026 for this release, on the merged code, with Go 1.24.7 on linux/amd64.

| File | What it holds |
|---|---|
| `go-test.log` | `go vet` and `go test` for every package, the tests of both lines of work included |
| `demos.log` | The eight demos run headless in Node with each demo's own code (`tools/verify.sh`, demo steps), with the times and the native-against-browser comparisons |
| `demo1.json` to `demo8.json` | Each headless run's figures, the ones the site and the README publish |
| `mcp-check-*.json` | Both official MCP clients against the native server, on each of the four demo files |
| `put3.log` to `put6.log`, `traces-native.log` | The native runs on the same data |
| `browser-check.log` | The eight demos in headless Chromium on the site's build |
| `release-check.log` | The amd64 release binary on the same data |

`demos.log` keeps Demo 6 twice: first with the file of the day before as 0.1.0 wrote it, then after it was rebuilt with 0.1.1. The first comparison found one difference, the version stamp in that file; the second found none.
