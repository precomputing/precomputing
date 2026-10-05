// Prepares agent runs for Demo 8 (Traces): the runs of one day, as a tracer beside the agent
// would see them.
//
// The input has the fields of nebius/SWE-rebench-openhands-trajectories, one run per line: the
// runs tools/traces-generate.mjs writes, or a sample of the real ones that
// tools/traces-from-parquet.py reads out of one of the dataset's Parquet files. For each run:
//
// - every message is written as the JSON the agent sent it in (role, content, tool_calls,
//   tool_call_id, name, in that order), and the tool list likewise; these bytes are what the
//   tracer stores and what a rebuilt call must match;
// - tokens are counted with the o200k_base tokenizer (js-tiktoken): a message's content and tool
//   calls, plus 4 tokens for the chat format, and the tool list as sent. The dataset has no token
//   counts, so these stand in for the usage a provider reports with each call;
// - the run is laid over 29 September 2026: a start time, and a time for every message from the
//   model's reply length and the tool that ran. The dataset has no times either.
//
// Usage: node tools/traces-prepare.mjs RUNS.jsonl [--runs N] [--source generated|nebius]
//          [--model NAME] [--out DIR]      (DIR defaults to demo/traces/app/data)
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { gzipSync } from 'node:zlib';
import { Tiktoken } from 'js-tiktoken/lite';
import o200k from 'js-tiktoken/ranks/o200k_base';

const args = process.argv.slice(2);
const opt = (name, def) => { const i = args.indexOf(name); return i >= 0 ? args[i + 1] : def; };
const input = args.find((a, i) => !a.startsWith('--') && !(i > 0 && args[i - 1].startsWith('--')));
const source = opt('--source', 'generated');
const model = opt('--model', source === 'nebius' ? 'Qwen3-Coder-480B-A35B-Instruct' : 'generated-coder');
const want = Number(opt('--runs', 0));
const out = opt('--out', new URL('../demo/traces/app/data/', import.meta.url).pathname);
mkdirSync(out, { recursive: true });

const enc = new Tiktoken(o200k);
const count = (s) => (s ? enc.encode(s).length : 0);

function mulberry32(seed) {
  let a = seed >>> 0;
  return function () {
    a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
const rnd = mulberry32(929);

export const DAY = Date.UTC(2026, 8, 29) / 1000;   // 29 September 2026, 00:00 UTC

// normalize writes a message in the chat format's field order, whatever shape it came in.
function normalize(m) {
  const msg = { role: m.role };
  let content = m.content;
  if (Array.isArray(content)) content = content.map((p) => (typeof p === 'string' ? p : p?.text ?? '')).join('');
  msg.content = content == null ? '' : String(content);
  const calls = typeof m.tool_calls === 'string' ? JSON.parse(m.tool_calls) : m.tool_calls;
  if (Array.isArray(calls) && calls.length) {
    msg.tool_calls = calls.map((c) => {
      const f = c.function || c;
      const a = f.arguments ?? f.args ?? {};
      return { id: String(c.id ?? ''), type: 'function', function: { name: String(f.name ?? ''), arguments: typeof a === 'string' ? a : JSON.stringify(a) } };
    });
  }
  if (m.tool_call_id) msg.tool_call_id = String(m.tool_call_id);
  if (m.name) msg.name = String(m.name);
  return msg;
}

const lines = readFileSync(input, 'utf8').split('\n').filter(Boolean);
let rows = lines.map((l) => JSON.parse(l));
if (want && want < rows.length) {
  // A sample spread over the whole file, the same one every time.
  const step = rows.length / want;
  rows = Array.from({ length: want }, (_, i) => rows[Math.floor(i * step)]);
}

// Start times: through the working day, 07:00 to 20:00 UTC, in the file's order, with the run that
// loops (the generated sample's run 46) at 11:10.
const starts = rows.map(() => rnd()).sort((a, b) => a - b).map((u) => DAY + 7 * 3600 + Math.floor(u * 13 * 3600));

const runs = [];
let rawBytes = 0, messages = 0;
for (const [k, row] of rows.entries()) {
  const traj = typeof row.trajectory === 'string' ? JSON.parse(row.trajectory) : row.trajectory;
  const toolsList = typeof row.tools === 'string' ? JSON.parse(row.tools) : row.tools || [];
  const tools = JSON.stringify(toolsList);
  const names = new Map(); // tool call id -> tool name
  let t = 0;
  let firstUser = true;
  const msgs = traj.map((m, i) => {
    const msg = normalize(m);
    for (const c of msg.tool_calls || []) names.set(c.id, c.function.name);
    const raw = JSON.stringify(msg);
    let src = msg.role;
    if (msg.role === 'user') { src = firstUser ? 'task' : 'user'; firstUser = false; }
    if (msg.role === 'tool') src = `tool:${msg.name || names.get(msg.tool_call_id) || 'unknown'}`;
    const tokens = count(msg.content) + (msg.tool_calls || []).reduce((a, c) => a + count(c.function.name) + count(c.function.arguments), 0) + 4;
    // Time: the model's reply takes about two seconds plus 40 tokens a second; a tool as long as
    // its work: tests by the line of output, other commands a few seconds, edits a moment.
    if (i >= 2) {
      if (msg.role === 'assistant') t += 1.5 + tokens / 40 + 2 * rnd();
      else if (msg.role === 'tool') {
        const tool = src.slice(5);
        const lines = msg.content.split('\n').length;
        t += /pytest/.test(msg.content.slice(0, 400)) || /test session starts/.test(msg.content.slice(0, 200)) ? 1.5 + lines / 60 + 3 * rnd()
          : tool === 'execute_bash' ? 0.3 + 2.5 * rnd() : tool === 'think' ? 0.05 : 0.1 + 0.4 * rnd();
      } else t += 1;
    }
    rawBytes += Buffer.byteLength(raw);
    messages++;
    return { raw, role: msg.role, source: src, tokens, at: Math.round(t) };
  });
  const id = String(row.trajectory_id ?? `run-${k + 1}`);
  runs.push({
    id, repo: String(row.repo ?? 'unknown'), task: String(row.instance_id ?? ''), model,
    start: id === 'generated-0046' ? DAY + 11 * 3600 + 600 : starts[k],
    resolved: row.resolved === true, exit: String(row.exit_status ?? ''),
    tools, tools_tokens: count(tools), messages: msgs,
  });
}
runs.sort((a, b) => a.start - b.start || a.id.localeCompare(b.id));
const text = runs.map((r) => JSON.stringify(r)).join('\n') + '\n';
const gz = gzipSync(text, { level: 9 });
writeFileSync(`${out}/day.jsonl.gz`, gz);
const calls = runs.reduce((a, r) => a + r.messages.filter((m, i) => i > 0 && m.role === 'assistant').length, 0);
const meta = {
  source,
  label: source === 'nebius'
    ? 'Agent runs from nebius/SWE-rebench-openhands-trajectories (Nebius, CC BY 4.0), laid over one day'
    : 'Agent runs generated by tools/traces-generate.mjs with the fields of nebius/SWE-rebench-openhands-trajectories; the repositories, issues and code are invented',
  credit: source === 'nebius' ? 'Contains agent trajectories from SWE-rebench-openhands-trajectories by Nebius, licensed under CC BY 4.0 (https://creativecommons.org/licenses/by/4.0/). Timestamps and token counts were added for this demo.' : null,
  model, day: '2026-09-29', runs: runs.length, calls, messages, repos: new Set(runs.map((r) => r.repo)).size,
  tokenizer: 'o200k_base (js-tiktoken 1.0.21)', messageBytes: rawBytes, fileBytes: Buffer.byteLength(text), gzBytes: gz.byteLength,
};
writeFileSync(`${out}/meta.json`, JSON.stringify(meta, null, 1) + '\n');
console.log(JSON.stringify(meta, null, 1));
