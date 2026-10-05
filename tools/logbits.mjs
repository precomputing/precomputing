// Compares ln() in SQLite's WebAssembly build with Go's math.Log, bit for bit, on the values
// that tools/logbits writes. The Engine in the browser relies on the two agreeing.
//
//   go run ./tools/logbits | node tools/logbits.mjs
import sqlite3InitModule from './node_modules/@sqlite.org/sqlite-wasm/dist/node.mjs';
import { readFileSync } from 'node:fs';

const buf = readFileSync(0);
const dv = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
const n = buf.byteLength / 16;
const sqlite3 = await sqlite3InitModule({ print: () => {}, printErr: () => {} });
const db = new sqlite3.oo1.DB(':memory:');
const st = db.prepare('SELECT ln(?)');
let differ = 0, example = null;
for (let i = 0; i < n; i++) {
  const x = dv.getFloat64(i * 16, true), g = dv.getFloat64(i * 16 + 8, true);
  st.bind(1, x); st.step(); const s = st.get(0); st.reset();
  if (!Object.is(s, g)) { differ++; if (!example) example = { x, sqlite: s, go: g }; }
}
st.finalize(); db.close();
console.log(`SQLite ${sqlite3.version.libVersion} WebAssembly: ${n} values, ${differ} differ from Go's math.Log`
  + (example ? `; first: ln(${example.x}) = ${example.sqlite} against ${example.go}` : ''));
process.exit(differ ? 1 : 0);
