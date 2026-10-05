// Demo 5's source: Wikimedia's public stream of recent changes, read as its schema
// (mediawiki/recentchange) describes it, and a simulator that writes events of the same shape for
// when the stream can't be reached. Works on a page, in a worker and in Node.

export const STREAM_URL = 'https://stream.wikimedia.org/v2/stream/recentchange';

// editorId turns a user name into a number (32-bit FNV-1a), so the file counts people without
// holding a name. The page sets a random salt when it opens, so the numbers mean nothing outside
// that visit; Node's runs keep 0, so their files come out the same every time.
let salt = 0;
export function setSalt(s) { salt = s >>> 0; }
export function editorId(name) {
  let h = (0x811c9dc5 ^ salt) >>> 0;
  for (let i = 0; i < name.length; i++) { h ^= name.charCodeAt(i); h = Math.imul(h, 0x01000193); }
  return h >>> 0;
}

const KINDS = new Set(['edit', 'new', 'log', 'categorize', 'external']);

// readChange takes one event of the stream and returns what the demo keeps of it: {canary: true}
// for the stream's own test events, null for anything it can't read. Of the user name only the
// number is kept, and the edit summary is never read.
export function readChange(e) {
  if (!e || typeof e !== 'object' || !e.meta || typeof e.meta !== 'object') return null;
  if (e.meta.domain === 'canary') return { canary: true };
  const ts = e.timestamp;
  const wiki = e.server_name;
  const kind = e.type;
  if (!Number.isInteger(ts) || typeof wiki !== 'string' || !wiki || !KINDS.has(kind)) return null;
  const len = e.length && typeof e.length === 'object' ? e.length : {};
  const bytes = (kind === 'edit' || kind === 'new') && Number.isInteger(len.new) ? len.new - (Number.isInteger(len.old) ? len.old : 0) : 0;
  return {
    id: typeof e.meta.id === 'string' ? e.meta.id : null,
    ts,
    wiki,
    kind,
    bot: e.bot === true,
    ns: Number.isInteger(e.namespace) ? e.namespace : null,
    title: typeof e.title === 'string' ? e.title : '',
    bytes,
    editor: typeof e.user === 'string' ? editorId(e.user) : 0,
  };
}

// An article: a page of the main namespace changed by an edit or a new page.
export const isArticle = (c) => c.ns === 0 && (c.kind === 'edit' || c.kind === 'new') && c.title !== '';

// pageUrl links a title on a wiki.
export const pageUrl = (wiki, title) => `https://${wiki}/wiki/${encodeURIComponent(title.replace(/ /g, '_')).replace(/%2F/g, '/').replace(/%3A/g, ':')}`;

export function mulberry32(seed) {
  let a = seed >>> 0;
  return function () {
    a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// The simulator's wikis: address, database name, share of changes, share made by bots. Invented
// proportions, roughly the shape of the real stream.
const WIKIS = [
  ['www.wikidata.org', 'wikidatawiki', 0.30, 0.55],
  ['en.wikipedia.org', 'enwiki', 0.22, 0.08],
  ['commons.wikimedia.org', 'commonswiki', 0.13, 0.35],
  ['de.wikipedia.org', 'dewiki', 0.05, 0.06],
  ['fr.wikipedia.org', 'frwiki', 0.05, 0.08],
  ['es.wikipedia.org', 'eswiki', 0.04, 0.06],
  ['en.wiktionary.org', 'enwiktionary', 0.03, 0.20],
  ['ja.wikipedia.org', 'jawiki', 0.03, 0.04],
  ['ru.wikipedia.org', 'ruwiki', 0.03, 0.07],
  ['it.wikipedia.org', 'itwiki', 0.03, 0.06],
  ['zh.wikipedia.org', 'zhwiki', 0.02, 0.05],
  ['pt.wikipedia.org', 'ptwiki', 0.02, 0.05],
  ['nl.wikipedia.org', 'nlwiki', 0.02, 0.10],
  ['pl.wikipedia.org', 'plwiki', 0.02, 0.06],
];
const SHARE = WIKIS.reduce((a, w) => a + w[2], 0);
// Invented places and ordinary things, so that a simulated title is never a real article's.
const HEADS = ['Ash', 'Bram', 'Cor', 'Dun', 'Eld', 'Fen', 'Gar', 'Hol', 'Ivel', 'Kel', 'Lin', 'Mor', 'Nor', 'Oak', 'Pen', 'Red', 'Sal', 'Thorn', 'Wick', 'Yar'];
const TAILS = ['wick', 'ford', 'ton', 'bury', 'dale', 'mouth', 'field', 'stead', 'more', 'ham'];
const THINGS = ['lighthouse', 'railway station', 'harbour', 'public library', 'bridge', 'castle', 'canal', 'museum', 'cathedral', 'waterfall',
  'observatory', 'botanical garden', 'windmill', 'lake', 'cave', 'aqueduct', 'tramway', 'ferry', 'airport', 'stadium', 'opera house',
  'market hall', 'clock tower', 'pier', 'tunnel', 'dam', 'town hall', 'football club', 'high school', 'festival'];
const topic = (r) => `${HEADS[Math.floor(r() * HEADS.length)]}${TAILS[Math.floor(r() * TAILS.length)]} ${THINGS[Math.floor(r() * THINGS.length)]}`;
const LOG_TYPES = [['upload', 'upload', 6], ['block', 'block', 2], ['delete', 'delete', 0], ['move', 'move', 0], ['protect', 'protect', 0],
  ['newusers', 'create', 2], ['patrol', 'patrol', 0], ['thanks', 'thank', 2]];

const hex = (r, n) => { let s = ''; for (let i = 0; i < n; i++) s += Math.floor(r() * 16).toString(16); return s; };
const uuid = (r) => `${hex(r, 8)}-${hex(r, 4)}-4${hex(r, 3)}-${'89ab'[Math.floor(r() * 4)]}${hex(r, 3)}-${hex(r, 12)}`;
const iso = (ts) => new Date(ts * 1000).toISOString().replace('.000Z', 'Z');

// Simulator writes invented changes shaped like the stream's, a second at a time: about 25 a
// second, a canary event now and then, and every few minutes a burst of people editing one article.
export class Simulator {
  constructor(seed = 5) {
    this.r = mulberry32(seed);
    this.offset = 1000000 + Math.floor(this.r() * 1000000);
    this.rcid = 2000000000 + Math.floor(this.r() * 100000000);
    this.rev = 1300000000 + Math.floor(this.r() * 100000000);
    this.bursts = [];
    this.nextBurst = null;
  }

  pickWiki() {
    let u = this.r() * SHARE;
    for (const w of WIKIS) { u -= w[2]; if (u < 0) return w; }
    return WIKIS[WIKIS.length - 1];
  }

  // second returns the events of one second, stamped ts.
  second(ts) {
    const r = this.r;
    if (this.nextBurst == null) this.nextBurst = ts + 30 + Math.floor(r() * 120);
    if (ts >= this.nextBurst) {
      const w = WIKIS[1 + Math.floor(r() * 6)];
      this.bursts.push({ wiki: w, title: topic(r), until: ts + 300 + Math.floor(r() * 400),
        people: Array.from({ length: 3 + Math.floor(r() * 6) }, () => `Editor ${1000 + Math.floor(r() * 90000)}`) });
      this.nextBurst = ts + 120 + Math.floor(r() * 240);
    }
    this.bursts = this.bursts.filter((b) => ts < b.until);
    const out = [];
    let n = 18 + Math.floor(r() * 15);
    while (n-- > 0) out.push(this.change(ts, this.pickWiki()));
    for (const b of this.bursts) if (r() < 0.06) out.push(this.change(ts, b.wiki, { title: b.title, user: b.people[Math.floor(r() * b.people.length)], bot: false, type: 'edit', ns: 0 }));
    if (r() < 1 / 60) out.push(this.canary(ts));
    return out;
  }

  change(ts, [server, db, , botShare], force = {}) {
    const r = this.r;
    const bot = force.bot ?? r() < botShare;
    const u = r();
    const type = force.type ?? (u < 0.74 ? 'edit' : u < 0.79 ? 'new' : u < 0.88 ? 'log' : 'categorize');
    let ns = force.ns ?? (r() < 0.72 ? 0 : [1, 2, 3, 4, 6, 10, 14][Math.floor(r() * 7)]);
    let title = force.title;
    if (!title) {
      if (db === 'wikidatawiki') title = `Q${1 + Math.floor(r() * 130000000)}`;
      else if (db === 'commonswiki') { ns = 6; title = `Example photo ${Math.floor(r() * 900000)}.jpg`; }
      else title = topic(r);
    }
    const prefix = { 1: 'Talk:', 2: 'User:', 3: 'User talk:', 4: 'Project:', 6: 'File:', 10: 'Template:', 14: 'Category:' }[ns] || '';
    const user = force.user ?? (bot ? `ExampleBot${1 + Math.floor(r() * 40)}` : r() < 0.15 ? `192.0.2.${1 + Math.floor(r() * 250)}` : `Editor ${1000 + Math.floor(r() * 90000)}`);
    const full = prefix + (prefix === 'User:' || prefix === 'User talk:' ? `Sandbox ${Math.floor(r() * 5000)}` : title);
    const url = `https://${server}/wiki/${encodeURIComponent(full.replace(/ /g, '_'))}`;
    const e = {
      $schema: '/mediawiki/recentchange/1.0.0',
      meta: { uri: url, request_id: uuid(r), id: uuid(r), dt: iso(ts), domain: server, stream: 'mediawiki.recentchange',
        topic: 'eqiad.mediawiki.recentchange', partition: 0, offset: this.offset++ },
      id: this.rcid++, type, namespace: ns, title: full, title_url: url,
      comment: 'Simulated change', timestamp: ts, user, bot,
      server_url: `https://${server}`, server_name: server, server_script_path: '/w', wiki: db, parsedcomment: 'Simulated change',
    };
    if (type === 'edit' || type === 'new') {
      const old = type === 'new' ? null : 2000 + Math.floor(r() * 60000);
      const delta = Math.round((r() - 0.35) * (r() < 0.05 ? 20000 : 600));
      e.minor = r() < 0.3;
      if (r() < 0.5) e.patrolled = r() < 0.6;
      e.length = { old, new: Math.max(0, (old ?? 0) + (type === 'new' ? Math.abs(delta) + 200 : delta)) };
      e.revision = { old: type === 'new' ? null : this.rev, new: ++this.rev };
      e.notify_url = `https://${server}/w/index.php?diff=${this.rev}`;
    } else if (type === 'log') {
      const [logType, action, lns] = LOG_TYPES[Math.floor(r() * LOG_TYPES.length)];
      Object.assign(e, { namespace: lns, log_id: 150000000 + Math.floor(r() * 1000000), log_type: logType, log_action: action,
        log_params: {}, log_action_comment: 'Simulated log entry' });
    }
    return e;
  }

  canary(ts) {
    const r = this.r;
    return { $schema: '/mediawiki/recentchange/1.0.0',
      meta: { uri: 'https://canary', request_id: uuid(r), id: uuid(r), dt: iso(ts), domain: 'canary', stream: 'mediawiki.recentchange',
        topic: 'eqiad.mediawiki.recentchange', partition: 0, offset: this.offset++ },
      id: 0, type: 'edit', namespace: 0, title: 'Canary', title_url: 'https://canary', comment: 'canary', timestamp: ts, user: 'canary',
      bot: false, server_url: 'https://canary', server_name: 'canary', server_script_path: '/w', wiki: 'canary' };
  }
}
