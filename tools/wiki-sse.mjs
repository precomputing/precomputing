// A stand-in for Wikimedia's stream of recent changes, for testing Demo 5 without the network:
// serves the simulator's events as server-sent events, the way stream.wikimedia.org does, with
// an id line per event and CORS open. Point the page at it with ?stream=http://127.0.0.1:8790/v2/stream/recentchange
// Usage: node tools/wiki-sse.mjs [--port 8790] [--rate 1]   (rate 2 sends two seconds of changes a second)
import http from 'node:http';
import { Simulator } from '../demo/live/app/wiki.js';

const arg = (name) => { const i = process.argv.indexOf(name); return i > 0 ? process.argv[i + 1] : null; };
const port = Number(arg('--port') || 8790);
const rate = Number(arg('--rate') || 1);
http.createServer((req, res) => {
  if (!req.url.startsWith('/v2/stream/recentchange')) { res.writeHead(404, { 'Access-Control-Allow-Origin': '*' }); res.end('not found'); return; }
  res.writeHead(200, { 'Content-Type': 'text/event-stream; charset=utf-8', 'Cache-Control': 'no-cache', 'Access-Control-Allow-Origin': '*', Connection: 'keep-alive' });
  res.write(':ok\n\n');
  const sim = new Simulator(Date.now() % 100000);
  let ts = Math.floor(Date.now() / 1000);
  const timer = setInterval(() => {
    for (let k = 0; k < rate; k++) {
      ts++;
      for (const e of sim.second(Math.min(ts, Math.floor(Date.now() / 1000)))) {
        const id = JSON.stringify([{ topic: e.meta.topic, partition: 0, offset: e.meta.offset }]);
        res.write(`event: message\nid: ${id}\ndata: ${JSON.stringify(e)}\n\n`);
      }
    }
  }, 1000);
  req.on('close', () => clearInterval(timer));
}).listen(port, '127.0.0.1', () => console.log(`recent changes, simulated, on http://127.0.0.1:${port}/v2/stream/recentchange`));
