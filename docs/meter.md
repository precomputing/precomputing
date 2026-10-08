# The Meter

The Meter counts usage for billing, such as tokens, calls or minutes, exactly. It is an exact stream with precomputes by customer and month, plus a quota. Either runtime runs it. The product's price list sits in the same SQLite file, so an invoice is one SQL view over numbers that are already added up. Version 0.1.1 also meters the model calls of AI agents, by run, repository, model and source, with a budget for each repository: see [Metering Agent Calls](#metering-agent-calls).

```text
stream usage exact {
  id     request_id refuse repeats 7d     # a retried request is counted once
  key    customer text
  key    model text
  key    gateway text
  value  input_tokens integer
  value  output_tokens integer
  derive tokens = input_tokens + output_tokens

  late   48h                              # up to two days late still counts, in the month it happened,
  period month close 24h                  # unless that month has closed: a month closes a day after it ends
  raw    until closed + 90d               # every request kept until its month has been closed 90 days
  rollup 1h keep 400d
  rollup 1d keep forever
}

precompute requests_month = count(usage) by customer, model per month
precompute input_month    = sum(usage.input_tokens) by customer, model per month
precompute output_month   = sum(usage.output_tokens) by customer, model per month

precompute tokens_month   = sum(usage.tokens) by customer per month
quota monthly_tokens = tokens_month
```

This is `examples/usage.precompute`, the policy behind Demo 3.

## What Each Rule Is For

**Retries.** A client that gets no answer in time sends the same request again, and a gateway that gets no acknowledgement sends its report again. The second report may come through another gateway. Each report carries the request's identifier, and the meter counts an identifier once. The repeat is refused and counted under `repeat` in `usage_refused`.

**Late reports.** A gateway that loses its link to the meter keeps serving and holds its reports. When the link comes back it sends them, and each request counts in the hour and the month it happened, up to two days late. In Demo 3 the eu gateway loses its link for six hours, and the gap in its hourly chart fills in when the link returns.

**The close.** A month closes a day after it ends. From then on nothing can change its totals, so an invoice sent on the 2nd stays true. A report that arrives after the close is refused as `closed` and counted, so the revenue it represents is known to the request. In Demo 3 a queue stuck in the us gateway is emptied five hours after the close, and all 642 of its reports are refused.

**Disputes.** Every request stays whole until its month has been closed for 90 days, so any invoice line can be checked request by request while a customer can still dispute it. After that, distill removes the requests and the invoice lines stay. The hourly and daily windows stay too, for charts and for comparing months.

**Quotas.** `monthly_tokens` shows each customer's use against its limit for every month. A gateway reads it before serving, one lookup by primary key, and turns a request away when `reached` is 1. A request turned away is never served, so it never reaches the meter. A customer without a row in `monthly_tokens_limit` has no limit.

## Invoices Are a View

The meter keeps token counts. Prices live in the product's own tables, beside the meter's, and money is kept in billionths of a dollar as integers, so every sum is exact. Demo 3's billing file (`demo/meter/app/billing.sql`) has four small tables, `plans`, `customers`, `prices` and `model_costs`, and two views:

```sql
CREATE VIEW invoice_lines AS
SELECT r.period, r.customer, r.model, r.value AS requests,
  CAST(i.value AS INTEGER) AS input_tokens, CAST(o.value AS INTEGER) AS output_tokens,
  CAST(i.value AS INTEGER) * p.input_nano + CAST(o.value AS INTEGER) * p.output_nano AS list_nano,
  CAST(i.value AS INTEGER) * k.input_nano + CAST(o.value AS INTEGER) * k.output_nano AS cost_nano
FROM requests_month r
JOIN input_month i USING (customer, model, period)
JOIN output_month o USING (customer, model, period)
JOIN prices p USING (model)
JOIN model_costs k USING (model);
```

`invoices` adds the lines up per customer and month, applies the plan's discount and rounds to the cent once, at the end. Reading every invoice of a month takes well under a millisecond, because the view reads a few dozen rows that the meter keeps current.

## Running It

With the SQL runtime, the meter is the compiled policy inside any SQLite database, and a gateway reports a request with one `INSERT`:

```sh
precomputing compile examples/usage.precompute | sqlite3 usage.db
sqlite3 usage.db "INSERT INTO usage (ts, request_id, customer, model, gateway, input_tokens, output_tokens)
                  VALUES (1790290800, 'r-81f2', 'harbor', 'large', 'eu', 2410, 880)"
sqlite3 usage.db "SELECT * FROM monthly_tokens"
```

With the Engine it is the same policy and the same file, about five times faster than the triggers in native SQLite. Every request is still written whole, which is most of the work. Sequence numbers make sure a crash loses nothing:

```sh
precomputing put --policy examples/usage.precompute --seq usage.db < reports.csv
precomputing serve --policy examples/usage.precompute usage.db      # or over HTTP
```

A CSV line is `seq,ts,request_id,customer,model,gateway,input_tokens,output_tokens`. `precomputing get usage.db tokens_month` prints a precompute, and any SQLite tool reads the rest.

## Measured

Demo 3 runs a month of invented usage: six customers on three plans, three models, two gateways. A client retry goes through either gateway. The eu link drops for six hours, and the us gateway has a queue that sticks at the month's end. The numbers below are from `node tools/run-demo3.mjs`, which runs the demo's own code with the same SQLite WebAssembly build the page uses, and from the native binary. The machine is a shared two-core cloud server (Intel Xeon at 2.8 GHz), where speeds change from one day to the next, in some runs by a third or more; where runs differed, the table gives their range.

| What | Result |
|---|---|
| The month | 366,118 requests served and 373,351 reports sent, retries included, through the compiled triggers in SQLite WebAssembly: 10,000 to 15,000 reports a second in Node and 8,500 to 13,600 in Chromium |
| Retries | 7,219 reports refused as repeats. No request id appears twice in the file |
| The six-hour outage | 625 held reports delivered when the link came back, each counted in its own hour |
| The stuck queue | 642 reports delivered after September closed, all refused as `closed`. The invoices did not change |
| September's invoices | $5,804.56 due against $3,337.84 of model cost. All 12 invoice lines and 6 amounts match a separate recount of the gateways' reports, to the billionth of a dollar, and match the requests kept in the file |
| Late reports | All 8,215 hourly windows match the recount, request for request and token for token, so every late report counts in the hour it happened |
| A quota check | One read of `monthly_tokens`: 10 to 13 microseconds in WebAssembly |
| Reading the invoices | Under 1 ms from the precomputes, against 0.36 to 0.61 seconds when computed from every request |
| The file | 25.3 MB at the end of the run, 1.9 MB after the dispute window with the invoices unchanged |
| The same reports through the compiled triggers in native SQLite | 10.7 to 15.4 seconds in our runs, 24,000 to 35,000 reports a second, committing every 10,000 with a full sync |
| The same reports through the native Engine | 2.3 to 3.3 seconds, 113,000 to 162,000 reports a second with a full sync at every checkpoint. Its tables equal the ones the demo's SQL runtime wrote in WebAssembly, value for value: 453,536 rows, 2,949,652 values, leaving out the price list and quota limits the demo adds |

The recount is separate code in the demo that takes the reports in the order the gateways sent them and applies the policy's three rules itself. It never reads the meter. To repeat the native comparison, `node tools/run-demo3.mjs --csv reports.csv --db sql.db` writes the reports and the SQL runtime's file, `precomputing put --seq` makes the Engine's, and `go run ./tools/filecompare -skip customers,model_costs,plans,prices,monthly_tokens_limit engine.db sql.db` compares the two, value by value, leaving out the price list and quota limits the demo adds to its file. `python3 tools/meter-triggers.py build/precomputing reports.csv` times the same reports through the compiled triggers in native SQLite.

## Metering Agent Calls

An AI agent works by calling a model again and again, and each call is billed by its tokens. The same kind of policy meters those calls. Each call is reported once, with its input, cached and output tokens, and its input is split by where it came from: the system prompt, the tool list, the task, the agent's own earlier messages and the output of each tool. The store of agent calls in Traces (see [Logs](logs.md#agent-traces)) sends these events as it keeps each call. Any other sender can send them the same way. The policy is `examples/traces.precompute`:

```text
stream calls exact {
  id     call_id refuse repeats 7d            # a call reported twice counts once
  key    repo text                            # the repository the agent works on
  key    run text                             # the agent run
  key    model text
  value  input_tokens integer
  value  cached_tokens integer                # input the provider had cached from the run's last call
  value  output_tokens integer
  derive cost_nano = (input_tokens - cached_tokens) * 400 + cached_tokens * 40 + output_tokens * 1600

  period day close 24h                        # a day closes a day after it ends
  raw    until closed + 30d                   # every call kept whole for 30 days after its day closes
  rollup 1h keep 400d
  rollup 1d keep forever
}

stream context exact {
  id     part_id refuse repeats 7d            # one event per call and source
  key    repo text
  key    source text                          # system, tools, task, assistant, output, or tool:NAME
  value  tokens integer                       # input tokens from this source
  value  cached_tokens integer
  value  output_tokens integer                # the reply, for the source output
  derive cost_nano = (tokens - cached_tokens) * 400 + cached_tokens * 40 + output_tokens * 1600

  period day close 24h
  raw    until closed + 30d
  rollup 1h keep 400d
}

precompute run_cost        = sum(calls.cost_nano) by run
precompute repo_cost_day   = sum(calls.cost_nano) by repo per day
quota repo_budget = repo_cost_day
precompute source_cost_day = sum(context.cost_nano) by source per day
```

**Two streams, one total.** `calls` gets one event for each call. `context` gets one for each source of the call's input and one for its output. Both derive the cost in billionths of a dollar from the same prices, so cost by call and cost by source add up to the same total. The prices are examples: $0.40 a million fresh input tokens, $0.04 for input the provider serves from its prompt cache, and $1.60 for output.

**A call reported twice.** A tracer that retries an upload sends a call again. Its events carry the same ids, `call_id` and `part_id`, and the exact streams refuse them as repeats, counted in `calls_refused` and `context_refused`.

**Budgets.** `repo_budget` is a quota on each repository's cost for the day. A gateway in front of the agents reads it before letting a run's next call through: one lookup by primary key. A repository without a row in `repo_budget_limit` has no budget.

Demo 8 runs a day of 100 coding-agent runs on 30 repositories, generated in the fields of a public dataset of real runs, and gives each repository $0.20 for the day. The numbers come from `node tools/run-demo8.mjs`, which runs the demo's own code with the page's builds, and from the native binary on the same server:

| What | Result |
|---|---|
| The day | 2,045 model calls: 21.4 million input tokens, 94% of them served from the cache, and 99,000 output tokens. $1.46 at the example prices |
| The recount | Separate code works out every call's tokens and cost from the runs and never reads the file. The cost of all 100 runs, the calls and tokens of every run, the calls and cost of all 30 repositories, the tokens and cost of all 8 sources and the cost by model: all equal, to the billionth of a dollar |
| The two streams | Cost by call and cost by source both come to $1.4554, the recount's total |
| A stuck run | 67 calls in 12 minutes for $0.46, about 46 times the median run. Its repository passed its $0.20 budget for the day at call 45, at 11:17:06. 46 calls came after the alert, 22 of them in the same run, for $0.30 |
| A run reported twice | 20 calls sent again: 20 refused in `calls_refused` and 136 source events in `context_refused`, and every cost unchanged |
| Native against the browser | `precomputing traces` keeps the same day in 0.8 to 1.0 seconds. Its file equals the browser's: 19 tables, 40,043 rows, 231,641 values, leaving out the budget limits the demo adds |

## Limits of Version 0.2.0

- The meter's clock is the newest event time it has counted. One report stamped far in the future moves that clock for every gateway and can close a month early, so gateways need correct clocks.
- One writer per file. Gateways report to one Engine process, over HTTP, or into one SQLite database that runs the triggers.
- Periods are UTC hours, days or months. A customer billed on another calendar can be billed from a per-day precompute that the billing view adds up.
- The customers, prices and plans in Demo 3 are invented examples.
- An agent call is metered with the tokens its sender reports. In Demo 8 they are counted with a tokenizer and the prompt cache is modeled; a provider's own usage report is the better source.
- A call's cost uses one price for each kind of token. Prices that differ by model or change over time belong in a price table beside the meter, as Demo 3's invoices do.
- The agent runs in Demo 8 are generated, and its prices are examples.
