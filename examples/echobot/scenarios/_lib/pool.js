// _lib/pool.js - reusable helpers for loading and picking from an NDJSON
// user pool. Env vars control filtering and selection so the same scenario
// works against different subsets without code changes.
//
//   TAG        require this tag to be present (default no filter)
//   OFFSET     skip this many records before forming the pool (default 0)
//   LIMIT      cap the pool size after offset (default = all)
//   SHUFFLE    if "1", shuffle once at run start before offset/limit
//
// The scenario itself owns `open()` so the file path resolves relative to
// the scenario script (e.g. '../data/users.ndjson' from a file in scenarios/),
// matching standard k6 semantics. Pass the resulting string to parsePool().

import { SharedArray } from 'k6/data';

// parsePool turns a raw NDJSON string into a SharedArray, honoring TAG /
// OFFSET / LIMIT / SHUFFLE env knobs. Call from a scenario like:
//
//   const users = parsePool(open('../data/users.ndjson'));
export function parsePool(rawNdjson) {
  return new SharedArray('telegym_pool', () => {
    let recs = rawNdjson
      .trim()
      .split('\n')
      .filter((l) => l.length > 0)
      .map((l) => JSON.parse(l));

    if (__ENV.TAG) {
      const tag = __ENV.TAG;
      recs = recs.filter((u) => (u.tags || []).indexOf(tag) >= 0);
    }
    if (__ENV.SHUFFLE === '1') {
      // Fisher-Yates so the order is deterministic per process but spread out.
      for (let i = recs.length - 1; i > 0; i--) {
        const j = Math.floor(Math.random() * (i + 1));
        [recs[i], recs[j]] = [recs[j], recs[i]];
      }
    }
    const offset = parseInt(__ENV.OFFSET || '0', 10);
    const limit = parseInt(__ENV.LIMIT || String(recs.length), 10);
    return recs.slice(offset, offset + limit);
  });
}

// pickUser selects one record from a pool. `mode` controls how:
//   'vu'     one user per VU (deterministic, repeatable across iterations)
//   'iter'   rotate per iteration (one VU touches many users)
//   'random' uniformly random per call
export function pickUser(pool, mode = 'vu') {
  if (!pool || pool.length === 0) {
    throw new Error('pickUser: pool is empty (check USER_FILE / TAG / LIMIT)');
  }
  const vu = (typeof __VU !== 'undefined' ? __VU : 1) - 1;
  const it = typeof __ITER !== 'undefined' ? __ITER : 0;
  switch (mode) {
    case 'vu':
      return pool[vu % pool.length];
    case 'iter':
      return pool[(vu + it) % pool.length];
    case 'random':
      return pool[Math.floor(Math.random() * pool.length)];
    default:
      throw new Error(`pickUser: unknown mode "${mode}"`);
  }
}
