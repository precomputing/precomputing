// Generates agent runs for Demo 8 with the same fields as nebius/SWE-rebench-openhands-trajectories:
// trajectory_id, instance_id, repo, trajectory (system, user, assistant and tool messages in the
// OpenAI chat format), tools, model_patch, exit_status and resolved. The repositories, issues, code
// and conversations are invented by this script with a fixed seed, so every run of it writes the
// same file. Every trajectory_id starts with "generated-".
//
// Three runs carry planted secrets (fake keys in the shapes real ones have), and one run is an agent
// stuck re-running a whole test suite, the way a run overspends. The rest are ordinary runs.
//
// Usage: node tools/traces-generate.mjs [--runs 100] > runs.jsonl

const arg = (name, def) => { const i = process.argv.indexOf(name); return i > 0 ? Number(process.argv[i + 1]) : def; };
const RUNS = arg('--runs', 100);

function mulberry32(seed) {
  let a = seed >>> 0;
  return function () {
    a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
const rnd = mulberry32(606);
const int = (a, b) => a + Math.floor(rnd() * (b - a + 1));
const pick = (xs) => xs[Math.floor(rnd() * xs.length)];
const chance = (p) => rnd() < p;
const alnum = (n, set = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789') => Array.from({ length: n }, () => set[Math.floor(rnd() * set.length)]).join('');

// The system prompt and the tools, the same in every run, as an agent framework sends them.
const SYSTEM = `You are a software engineering agent. You work inside a sandboxed Linux machine with a Python repository checked out under /workspace, and you resolve the issue the user describes by reading the code, running commands and editing files.

<ROLE>
Your job is to make the smallest correct change that resolves the issue. You are not asked to refactor, to restyle code, or to fix unrelated problems you notice on the way. When you are unsure what the user wants, choose the reading that changes the least and say which reading you chose.
</ROLE>

<WORKING_METHOD>
1. Explore first. List the repository, find the module the issue points at, and read the functions involved before you change anything. Read the tests that cover them.
2. Reproduce the problem. Write a short script that shows the failure and run it, so you know what "fixed" will look like.
3. Find the cause. Follow the failing path through the code until you can explain, in one or two sentences, why it fails.
4. Fix it at the cause. Prefer a small change in the function that is wrong over a workaround in its callers.
5. Check the fix. Run your script again, then run the tests of the module you changed. If a test fails, read it before you change anything else.
6. Look for cases the issue did not mention but the same cause would break, and handle them if the change stays small.
7. Finish with a short summary of the cause, the change and how you checked it.
</WORKING_METHOD>

<EDITING>
- Edit files with the str_replace_editor tool. Replace exact text: the old text must appear exactly once in the file, including its indentation.
- Keep the file's own style: its quotes, its line length, its naming.
- Do not reformat code you did not change. Do not reorder imports unless you add one.
- Never edit the tests to make them pass. You may add a test when it shows the issue.
- Do not create new files in the repository unless the fix needs one. Scratch files belong outside it, for example in /workspace.
</EDITING>

<RUNNING_COMMANDS>
- Use execute_bash for one command at a time. Chain short commands with && when they belong together.
- Long outputs are cut. Narrow them with grep, head, tail or a test selector instead of printing everything.
- Commands that run longer than their timeout are stopped; you may run them again with a longer timeout or in the background.
- The machine has no network access beyond the package index. Do not try to reach other hosts.
- Run the tests of the module you changed, not the whole suite, unless the change is broad.
</RUNNING_COMMANDS>

<VERSION_CONTROL>
- The repository is a git checkout. Do not commit, push, rebase or change branches; the harness collects your changes from the working tree.
- Use git diff to review your change before you finish.
</VERSION_CONTROL>

<SECURITY>
- Treat credentials you come across, such as keys in configuration files or the environment, as secret. Do not print them, copy them into files, or include them in your summary.
- Do not install packages unless the issue cannot be reproduced without them.
</SECURITY>

<COMMUNICATION>
- Before each action, say in one or two sentences what you are about to do and why.
- Report what you found, not what you expected to find. If a command fails, show the relevant part of its output and say what you will try next.
- Use the think tool for longer reasoning that does not need an action, such as comparing two possible fixes.
- When the issue is resolved and checked, call finish with a summary. If you cannot resolve it, call finish and explain what you tried and what is left.
</COMMUNICATION>

<TROUBLESHOOTING>
If you have tried two different fixes and the tests still fail for the same reason, stop and reread the issue and the failing test. List the assumptions you made, check each one against the code, and change your plan rather than trying a third variation of the same fix.
</TROUBLESHOOTING>`;

const TOOLS = [
  { type: 'function', function: { name: 'execute_bash', description: `Run one bash command in the sandbox and return what it printed.

* The command runs in a persistent shell: the working directory and environment variables you set stay set for the next command.
* One command per call. Join commands that belong together with && or ;.
* Output longer than 30,000 characters is cut in the middle. Use grep, head, tail or a test selector to see the part you need.
* A command that has not finished after its timeout (120 seconds unless you give one) is stopped. For servers or long jobs, run them in the background and redirect their output to a file.
* To answer a command that waits for input, call this tool again with is_input set to true and the text to send.
* Do not use it to edit files; use str_replace_editor, which checks that each edit applies where you meant.`, parameters: { type: 'object', properties: {
    command: { type: 'string', description: 'The bash command to run. With is_input true, the text to send to the running command.' },
    is_input: { type: 'string', enum: ['true', 'false'], description: 'true to send input to a running command instead of starting a new one. Default false.' },
    timeout: { type: 'number', description: 'Seconds to wait before the command is stopped. Default 120.' },
  }, required: ['command'] } } },
  { type: 'function', function: { name: 'str_replace_editor', description: `View, create and edit files.

* view shows a file with line numbers, or lists a directory two levels deep. For a long file, pass view_range to see part of it.
* create writes a new file with file_text. It refuses to overwrite a file that exists.
* str_replace replaces old_str with new_str. old_str must match the file exactly, whitespace included, and must appear exactly once; include a few lines of context to make it unique.
* insert adds new_str after line insert_line.
* undo_edit reverts the last edit made to the file.

Paths are absolute. Every edit reports the lines around the change so that you can check it applied as you meant.`, parameters: { type: 'object', properties: {
    command: { type: 'string', enum: ['view', 'create', 'str_replace', 'insert', 'undo_edit'], description: 'What to do.' },
    path: { type: 'string', description: 'Absolute path of the file or directory, such as /workspace/project/module.py.' },
    file_text: { type: 'string', description: 'For create: the whole content of the new file.' },
    old_str: { type: 'string', description: 'For str_replace: the exact text to replace.' },
    new_str: { type: 'string', description: 'For str_replace: the text that replaces old_str. For insert: the text to add.' },
    insert_line: { type: 'integer', description: 'For insert: the line after which new_str goes.' },
    view_range: { type: 'array', items: { type: 'integer' }, description: 'For view: the first and last line to show, such as [40, 80]. [40, -1] shows from line 40 to the end.' },
  }, required: ['command', 'path'] } } },
  { type: 'function', function: { name: 'think', description: `Write down reasoning that needs no action: weighing two fixes, listing what a failing test tells you, or planning the next steps. Nothing in the sandbox changes. Use it when a problem has several parts, or after an attempt that did not work.`, parameters: { type: 'object', properties: {
    thought: { type: 'string', description: 'The reasoning to record.' },
  }, required: ['thought'] } } },
  { type: 'function', function: { name: 'finish', description: `End the task. Give a short summary: the cause of the issue, what you changed, and how you checked it. If you could not resolve the issue, say what you tried and what remains.`, parameters: { type: 'object', properties: {
    message: { type: 'string', description: 'The summary for the user.' },
    task_completed: { type: 'string', enum: ['true', 'false', 'partial'], description: 'Whether the issue is resolved.' },
  }, required: ['message', 'task_completed'] } } },
];

// Invented repositories: an owner and a project name made of two syllables.
const OWNERS = ['fernwick', 'brambleworks', 'quillon-dev', 'marrowgate', 'saltmarsh-io', 'tallowcraft'];
const HEADS = ['ember', 'loch', 'tarn', 'gild', 'marl', 'rill', 'sable', 'thistle', 'umber', 'wren', 'yarrow', 'kiln', 'lumen', 'oriel', 'pike', 'vale'];
const TAILS = ['parse', 'cache', 'forms', 'graph', 'sync', 'time', 'kit', 'flow', 'table', 'query'];
const REPOS = [];
while (REPOS.length < 30) {
  const name = pick(HEADS) + pick(TAILS);
  if (!REPOS.some((r) => r.name === name)) REPOS.push({ owner: pick(OWNERS), name, version: `${int(0, 3)}.${int(1, 19)}.${int(0, 9)}` });
}

// Functions a module may hold, as source code, with the kind of bug an issue can report.
const FUNCS = [
  { name: 'parse_duration', err: 'ValueError', when: 'the text has surrounding whitespace', example: '" 90s"', expected: '90', src: [
    'def parse_duration(text):', '    """Return the number of seconds in a duration such as "90s", "5m" or "2h"."""',
    '    units = {"s": 1, "m": 60, "h": 3600, "d": 86400}', '    number, unit = text[:-1], text[-1]', '    if unit not in units:',
    '        raise ValueError(f"unknown unit in {text!r}")', '    return int(number) * units[unit]'] },
  { name: 'normalize_path', err: 'IndexError', when: 'the path is empty', example: '""', expected: '"."', src: [
    'def normalize_path(path, sep="/"):', '    """Collapse repeated separators and remove a trailing one."""', '    parts = [p for p in path.split(sep) if p]',
    '    joined = sep.join(parts)', '    if path[0] == sep:', '        joined = sep + joined', '    return joined'] },
  { name: 'chunked', err: 'ZeroDivisionError', when: 'the size is zero', example: '[1, 2, 3], 0', expected: 'a clear ValueError', src: [
    'def chunked(items, size):', '    """Split items into lists of at most size elements."""', '    count = (len(items) + size - 1) // size',
    '    return [items[i * size:(i + 1) * size] for i in range(count)]'] },
  { name: 'merge_settings', err: 'TypeError', when: 'a nested value is None', example: '{"db": {"port": 5432}}, {"db": None}', expected: '{"db": None}', src: [
    'def merge_settings(base, override):', '    """Merge override into a copy of base, recursing into nested dicts."""', '    result = dict(base)',
    '    for key, value in override.items():', '        if key in result and isinstance(result[key], dict):', '            result[key] = merge_settings(result[key], value)',
    '        else:', '            result[key] = value', '    return result'] },
  { name: 'to_snake_case', err: 'AssertionError', when: 'the name has an acronym', example: '"HTTPServer"', expected: '"http_server"', src: [
    'def to_snake_case(name):', '    """Turn CamelCase into snake_case."""', '    out = []', '    for i, ch in enumerate(name):',
    '        if ch.isupper() and i > 0:', '            out.append("_")', '        out.append(ch.lower())', '    return "".join(out)'] },
  { name: 'parse_range', err: 'ValueError', when: 'the range is open at one end', example: '"5-"', expected: '(5, None)', src: [
    'def parse_range(text):', '    """Parse "a-b" into a pair of integers."""', '    low, high = text.split("-")', '    return int(low), int(high)'] },
  { name: 'read_rows', err: 'UnicodeDecodeError', when: 'the file starts with a byte order mark', example: '"data.csv"', expected: 'the first column name without the mark', src: [
    'def read_rows(path, delimiter=","):', '    """Read a delimited file into a list of dicts."""', '    with open(path, encoding="ascii") as f:',
    '        header = f.readline().rstrip("\\n").split(delimiter)', '        return [dict(zip(header, line.rstrip("\\n").split(delimiter))) for line in f]'] },
  { name: 'retry', err: 'RecursionError', when: 'attempts is large', example: 'flaky, attempts=2000', expected: 'up to 2000 attempts', src: [
    'def retry(func, attempts=3, delay=0.0):', '    """Call func until it succeeds, at most attempts times."""', '    try:', '        return func()', '    except Exception:',
    '        if attempts <= 1:', '            raise', '        time.sleep(delay)', '        return retry(func, attempts - 1, delay)'] },
  { name: 'format_bytes', err: 'KeyError', when: 'the size is a petabyte or more', example: '2 ** 51', expected: '"2.0 PB"', src: [
    'def format_bytes(size):', '    """Format a size in bytes for people: 1536 -> "1.5 KB"."""', '    units = ["B", "KB", "MB", "GB", "TB"]', '    power = 0',
    '    while size >= 1024:', '        size /= 1024', '        power += 1', '    return f"{size:.1f} {units[power]}"'] },
  { name: 'flatten', err: 'TypeError', when: 'an item is a string', example: '[["a", "b"], "cd"]', expected: '["a", "b", "cd"]', src: [
    'def flatten(items):', '    """Flatten nested lists and tuples into one list."""', '    out = []', '    for item in items:', '        try:',
    '            out.extend(flatten(item))', '        except TypeError:', '            out.append(item)', '    return out'] },
];
const MODULES = ['core', 'utils', 'parser', 'io', 'config', 'types', 'cache', 'timeutil', 'validators', 'cli', 'formats', 'registry'];
const FILLER = [
  ['class Registry:', '    """Keeps named items and looks them up."""', '', '    def __init__(self):', '        self._items = {}', '',
    '    def add(self, name, item):', '        if name in self._items:', '            raise KeyError(f"{name} is already registered")', '        self._items[name] = item', '',
    '    def get(self, name, default=None):', '        return self._items.get(name, default)', '', '    def __len__(self):', '        return len(self._items)'],
  ['def is_blank(text):', '    """True when text is None or only white space."""', '    return text is None or not text.strip()'],
  ['def clamp(value, low, high):', '    """Keep value between low and high."""', '    return max(low, min(high, value))'],
  ['def first(items, default=None):', '    """The first item, or default when there is none."""', '    for item in items:', '        return item', '    return default'],
  ['def unique(items):', '    """Items in order, each once."""', '    seen = set()', '    out = []', '    for item in items:', '        if item not in seen:',
    '            seen.add(item)', '            out.append(item)', '    return out'],
  ['def load_json(path):', '    """Read a JSON file."""', '    with open(path, encoding="utf-8") as f:', '        return json.load(f)'],
  ['class Timer:', '    """Measure how long a block takes."""', '', '    def __enter__(self):', '        self.start = time.perf_counter()', '        return self', '',
    '    def __exit__(self, *exc):', '        self.elapsed = time.perf_counter() - self.start', '        return False'],
];

function moduleSource(pkg, fn) {
  const lines = [`"""${pkg}: ${pick(['helpers', 'small utilities', 'shared functions', 'parsing and formatting'])} used across the package."""`, '',
    'import json', 'import os', 'import re', 'import time', '', `__all__ = ["${fn.name}"]`, ''];
  const parts = [...FILLER].sort(() => rnd() - 0.5).slice(0, int(2, 5));
  const at = int(0, parts.length);
  parts.splice(at, 0, fn.src);
  for (const p of parts) lines.push('', ...p, '');
  // Longer modules: more of the same, as real modules are.
  while (lines.length < int(60, 180)) lines.push('', ...pick(FILLER), '');
  return lines;
}

function testSource(pkg, mod, fn) {
  const lines = ['import pytest', '', `from ${pkg}.${mod} import ${fn.name}`, ''];
  for (let k = int(6, 16); k > 0; k--) {
    const name = pick(['basic', 'empty', 'unicode', 'nested', 'large', 'roundtrip', 'invalid', 'defaults', 'edge', 'regression', 'types', 'order']);
    lines.push('', `def test_${fn.name}_${name}_${k}():`, `    value = ${fn.name}(${pick(['"a"', '[]', '{}', '0', '"x y"', '[1, 2, 3]', '"5m"', '"/tmp//a/"'])})`,
      `    assert value ${pick(['==', '!=', 'is not'])} ${pick(['None', '0', '[]', '"a"', '300', '"/tmp/a"'])}`);
    if (chance(0.3)) lines.push('', `@pytest.mark.parametrize("raw", [${Array.from({ length: int(2, 6) }, () => `"${alnum(int(1, 6), 'abcdefghij0123456789')}"`).join(', ')}])`,
      `def test_${fn.name}_accepts_${k}(raw):`, `    ${fn.name}(raw)`);
  }
  return lines;
}

const numbered = (lines, from = 1) => lines.map((l, i) => `${String(i + from).padStart(6)}\t${l}`).join('\n');

function testOutput(pkg, mod, n, failed, verbose) {
  const tests = Array.from({ length: n }, (_, i) => `tests/test_${mod}.py::test_${pick(['basic', 'empty', 'unicode', 'nested', 'large', 'roundtrip', 'invalid', 'defaults', 'edge', 'regression'])}_${i}`);
  const lines = [`============================= test session starts ==============================`, `platform linux -- Python 3.11.9, pytest-8.3.2, pluggy-1.5.0`, `rootdir: /workspace/${pkg}`, `configfile: pyproject.toml`, `collected ${n} items`, ''];
  if (verbose) {
    tests.forEach((t, i) => lines.push(`${t} ${failed.includes(i) ? 'FAILED' : 'PASSED'}${' '.repeat(Math.max(1, 60 - t.length))}[${String(Math.round((100 * (i + 1)) / n)).padStart(3)}%]`));
  } else {
    let row = `tests/test_${mod}.py `;
    tests.forEach((_, i) => { row += failed.includes(i) ? 'F' : '.'; });
    lines.push(`${row} [100%]`);
  }
  for (const i of failed) {
    lines.push('', `=================================== FAILURES ===================================`, `________________________ ${tests[i].split('::')[1]} ________________________`, '',
      `    def ${tests[i].split('::')[1]}():`, `>       assert result == expected`, `E       AssertionError: assert ${int(1, 99)} == ${int(100, 999)}`, '', `tests/test_${mod}.py:${int(10, 300)}: AssertionError`);
  }
  lines.push(`=========================== short test summary info ============================`);
  for (const i of failed) lines.push(`FAILED ${tests[i]} - AssertionError`);
  lines.push(`${failed.length ? `${failed.length} failed, ` : ''}${n - failed.length} passed in ${(0.05 * n + rnd()).toFixed(2)}s`);
  return lines.join('\n');
}

let callNo = 0;
function run(index, special) {
  const repo = special?.repo || pick(REPOS);
  const pkg = repo.name, fn = pick(FUNCS), mod = pick(MODULES);
  const path = `/workspace/${pkg}/${pkg}/${mod}.py`;
  const src = moduleSource(pkg, fn);
  const fnLine = src.findIndex((l) => l.startsWith(`def ${fn.name}(`)) + 1;
  const issueNo = int(40, 2400);
  const traj = [{ role: 'system', content: SYSTEM }];
  const task = `The repository ${repo.owner}/${pkg} is checked out at /workspace/${pkg}. Please resolve this issue from its tracker.

<issue>
\`${fn.name}\` raises ${fn.err} when ${fn.when}

Calling \`${pkg}.${mod}.${fn.name}(${fn.example})\` fails:

\`\`\`
Traceback (most recent call last):
  File "<stdin>", line 1, in <module>
  File "/usr/lib/python3.11/site-packages/${pkg}/${mod}.py", line ${fnLine + int(2, 5)}, in ${fn.name}
${fn.err}: ${pick(['invalid literal for int() with base 10', 'string index out of range', 'integer division or modulo by zero', 'unsupported operand type(s)', 'maximum recursion depth exceeded', "'PB'", 'assertion failed'])}
\`\`\`

I expected ${fn.expected}. This is ${pkg} ${repo.version} on Python 3.11.
</issue>

The tests for this issue are already in place, so leave the test files as they are. Change only the package's own code, as little as the fix needs, and check your change before you finish.`;
  traj.push({ role: 'user', content: task });
  const step = (thought, name, args, output) => {
    const id = `call_${(++callNo).toString(36).padStart(6, '0')}`;
    traj.push({ role: 'assistant', content: thought, tool_calls: [{ id, type: 'function', function: { name, arguments: JSON.stringify(args) } }] });
    if (output != null) traj.push({ role: 'tool', content: output, tool_call_id: id, name });
  };
  const files = MODULES.map((m) => `/workspace/${pkg}/${pkg}/${m}.py`).concat(MODULES.slice(0, int(4, 9)).map((m) => `/workspace/${pkg}/tests/test_${m}.py`),
    [`/workspace/${pkg}/pyproject.toml`, `/workspace/${pkg}/README.md`, `/workspace/${pkg}/${pkg}/__init__.py`]);

  step(`I'll start by looking at the layout of the repository to find where \`${fn.name}\` lives.`, 'execute_bash',
    { command: `find /workspace/${pkg} -type f -not -path '*/.git/*' | sort | head -60` }, files.sort().join('\n'));
  if (special?.secret === 'env') {
    step('Before running anything, let me check which environment variables the test configuration reads.', 'execute_bash', { command: 'env | sort' },
      ['HOME=/root', `HOSTNAME=sandbox-${alnum(8, 'abcdef0123456789')}`, 'LANG=C.UTF-8', 'PATH=/usr/local/bin:/usr/bin:/bin', 'PYTHONDONTWRITEBYTECODE=1',
        `AWS_ACCESS_KEY_ID=AKIA${alnum(16, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567')}`, `AWS_SECRET_ACCESS_KEY=${alnum(40, 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789/+')}`,
        'AWS_DEFAULT_REGION=eu-west-1', `PWD=/workspace/${pkg}`, 'SHLVL=1', 'TERM=xterm'].join('\n'));
  }
  if (special?.secret === 'git') {
    step('Let me check where the checkout came from, in case the issue is fixed upstream already.', 'execute_bash', { command: `cat /workspace/${pkg}/.git/config` },
      `[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n[remote "origin"]\n\turl = https://x-access-token:ghp_${alnum(36)}@github.example/${repo.owner}/${pkg}.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[branch "main"]\n\tremote = origin\n\tmerge = refs/heads/main`);
  }
  step(`The function is probably in ${mod}.py. Let me open it.`, 'str_replace_editor', { command: 'view', path }, `Contents of ${path}, with line numbers:\n${numbered(src)}\n`);
  if (special?.secret === 'key') {
    const cfg = `/workspace/${pkg}/${pkg}/settings_local.py`;
    step('The module reads defaults from a local settings file. Let me look at it.', 'str_replace_editor', { command: 'view', path: cfg },
      `Contents of ${cfg}, with line numbers:\n${numbered(['"""Local settings for development. Not for production."""', '', 'DEBUG = True', 'CACHE_SIZE = 512',
        `OPENAI_API_KEY = "sk-proj-${alnum(48, 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-')}"`, 'TIMEOUT_SECONDS = 30'])}\n`);
  }
  const reads = int(3, 12);
  for (let k = 0; k < reads; k++) {
    const other = pick(MODULES);
    const what = rnd();
    if (what < 0.4) {
      const p2 = `/workspace/${pkg}/${pkg}/${other}.py`;
      step(pick([`Let me read ${other}.py too; it may call \`${fn.name}\` in a way that matters.`, `${other}.py imports from ${mod}.py. Let me see what it expects.`,
        `Before changing anything I want to understand how ${other}.py uses these helpers.`]), 'str_replace_editor', { command: 'view', path: p2 },
      `Contents of ${p2}, with line numbers:\n${numbered(moduleSource(pkg, pick(FUNCS)))}\n`);
      continue;
    }
    if (what < 0.65) {
      const tp = `/workspace/${pkg}/tests/test_${mod}.py`;
      step(pick([`Let me look at the existing tests for ${mod}.py, to see which cases they already cover.`, 'The tests show what behavior the maintainers expect. Let me read them.']),
        'str_replace_editor', { command: 'view', path: tp }, `Contents of ${tp}, with line numbers:\n${numbered(testSource(pkg, mod, fn))}\n`);
      continue;
    }
    step(pick([`Let me see how ${other}.py uses it.`, `I want to check the callers in ${other}.py as well.`, `Let me look for other uses of \`${fn.name}\`.`]), 'execute_bash',
      { command: `grep -rn "${fn.name}" /workspace/${pkg} --include=*.py | head -30` },
      Array.from({ length: int(2, 9) }, () => `/workspace/${pkg}/${pkg}/${pick(MODULES)}.py:${int(3, 240)}:    ${pick(['value = ', 'result = ', 'return ', 'items = '])}${fn.name}(${pick(['text', 'path', 'items, 10', 'config', 'name', 'raw'])})`).join('\n'));
  }
  const repro = [`from ${pkg}.${mod} import ${fn.name}`, '', `print(${fn.name}(${fn.example}))`];
  step('Let me write a script that reproduces the issue.', 'str_replace_editor', { command: 'create', path: '/workspace/reproduce_issue.py', file_text: repro.join('\n') + '\n' },
    'File created successfully at: /workspace/reproduce_issue.py');
  step('Now I run it to see the failure.', 'execute_bash', { command: `cd /workspace/${pkg} && python /workspace/reproduce_issue.py` },
    `Traceback (most recent call last):\n  File "/workspace/reproduce_issue.py", line 3, in <module>\n    print(${fn.name}(${fn.example}))\n  File "/workspace/${pkg}/${pkg}/${mod}.py", line ${fnLine + int(2, 5)}, in ${fn.name}\n${fn.err}: the issue's error`);
  if (chance(0.5)) {
    step('Let me think about where to fix it.', 'think', { thought: `The failure comes from ${fn.name} itself: it does not handle the case where ${fn.when}. Fixing it in the function keeps every caller correct; the alternative, checking in each caller, would repeat the check ${int(2, 6)} times. I'll change ${fn.name} and keep its signature.` },
      'Your thought has been logged.');
  }
  const fixAt = fnLine + int(1, fn.src.length - 2);
  const edits = 1 + (chance(0.5) ? 1 : 0) + (chance(0.3) ? 1 : 0) + (chance(0.15) ? 1 : 0);
  let failures = edits - 1;
  for (let e = 0; e < edits; e++) {
    const old = src[fixAt - 1] ?? '    return result';
    if (e > 0 || chance(0.5)) {
      step(pick(['Let me look at the function again, around the line I need to change.', 'Before editing, let me see the exact lines, indentation included.']), 'str_replace_editor',
        { command: 'view', path, view_range: [Math.max(1, fnLine - 2), fnLine + fn.src.length + 2] },
        `Contents of ${path}, lines ${Math.max(1, fnLine - 2)} to ${fnLine + fn.src.length + 2}:\n${numbered(src.slice(Math.max(0, fnLine - 3), fnLine + fn.src.length + 2), Math.max(1, fnLine - 2))}\n`);
    }
    step(e ? 'That was not enough. Let me adjust the fix.' : `I'll fix ${fn.name} at the cause.`, 'str_replace_editor',
      { command: 'str_replace', path, old_str: old, new_str: `${old.replace(/\S.*$/, '')}# handle the case the issue reports\n${old}` },
      `Edited ${path}. Lines ${fixAt - 3} to ${fixAt + 4} now read:\n${numbered(src.slice(Math.max(0, fixAt - 4), fixAt + 4), Math.max(1, fixAt - 3))}\nCheck that the change is what you meant; edit again if it is not.`);
    const n = int(8, 40);
    const failed = failures-- > 0 ? [int(0, n - 1)] : [];
    step('Let me run the script again, and then the tests of this module.', 'execute_bash', { command: `cd /workspace/${pkg} && python /workspace/reproduce_issue.py && python -m pytest tests/test_${mod}.py -q` },
      `${fn.expected.replace(/^"|"$/g, '')}\n${testOutput(pkg, mod, n, failed, false)}`);
  }
  if (special?.runaway) {
    // Stuck: the agent keeps running the whole suite verbosely, hoping to see a flaky test fail.
    for (let k = 0; k < special.runaway; k++) {
      const n = int(380, 460);
      step(pick(['The whole suite may tell me more. Let me run everything with verbose output.', 'One test was flaky earlier. Let me run the full suite again to see whether it fails.',
        'I still see the same result. Let me run all the tests once more.', 'Let me run the complete suite verbosely again.']), 'execute_bash',
      { command: `cd /workspace/${pkg} && python -m pytest -v` }, testOutput(pkg, pick(MODULES), n, chance(0.2) ? [int(0, n - 1)] : [], true));
    }
  } else if (chance(0.45)) {
    step('Let me also run the tests of the modules that call it.', 'execute_bash', { command: `cd /workspace/${pkg} && python -m pytest tests -q -x` },
      testOutput(pkg, pick(MODULES), int(60, 220), [], false));
  }
  step('Let me review the change.', 'execute_bash', { command: `cd /workspace/${pkg} && git diff` },
    `diff --git a/${pkg}/${mod}.py b/${pkg}/${mod}.py\n--- a/${pkg}/${mod}.py\n+++ b/${pkg}/${mod}.py\n@@ -${fixAt},3 +${fixAt},4 @@\n+    # handle the case the issue reports\n ${src[fixAt - 1] ?? ''}`);
  const done = !special?.runaway && chance(0.72);
  step('The fix is in place and checked.', 'finish', { message: done ? `The cause was that ${fn.name} did not handle the case where ${fn.when}. I changed ${fn.name} in ${mod}.py to handle it, reproduced the issue before the change and confirmed the fix after it, and the tests of ${mod}.py pass.` : `I changed ${fn.name} in ${mod}.py, but I could not confirm that every case in the issue is handled.`, task_completed: done ? 'true' : 'partial' }, null);
  return {
    trajectory_id: `generated-${String(index).padStart(4, '0')}`,
    instance_id: `${repo.owner}__${pkg}-${issueNo}`,
    repo: `${repo.owner}/${pkg}`,
    trajectory: traj,
    tools: TOOLS,
    model_patch: `diff --git a/${pkg}/${mod}.py b/${pkg}/${mod}.py\n--- a/${pkg}/${mod}.py\n+++ b/${pkg}/${mod}.py\n@@ -${fixAt},3 +${fixAt},4 @@\n+    # handle the case the issue reports\n`,
    exit_status: 'submitted',
    resolved: done && chance(0.8),
  };
}

// Which runs are special: three secrets, and one agent stuck in a loop on the busiest repository.
const specials = new Map([[17, { secret: 'env' }], [58, { secret: 'git' }], [83, { secret: 'key' }], [46, { runaway: 44, repo: REPOS[3] }]]);
for (let i = 1; i <= RUNS; i++) process.stdout.write(JSON.stringify(run(i, specials.get(i))) + '\n');
