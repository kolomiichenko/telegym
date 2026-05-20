// scenarios/echo_pool_replay.js - second half of the pool demo.
// Loads users registered by echo_pool.js and replays /start as them.
// Demonstrates pool reuse: same chat IDs, deterministic per-VU mapping.

import tg from 'k6/x/telegym';
import { check } from 'k6';
import { parsePool, pickUser } from './_lib/pool.js';

const users = parsePool(open('../data/echo-users.ndjson'));

export const options = {
  scenarios: {
    replay: {
      executor: 'constant-vus',
      vus: 20,
      duration: '20s',
    },
  },
  thresholds: {
    checks: ['rate>0.99'],
  },
};

export default function () {
  const d = pickUser(users, 'iter');     // rotate per iteration
  const u = tg.newUser(d.chat_id);       // reuse registered chat ID
  u.send('/start');
  const reply = u.awaitText('^Hello', 5);
  check(reply, {
    'reply arrived': (m) => m !== null,
    'reply under 200ms': (m) => m && m.latencyMs < 200,
  });
}
