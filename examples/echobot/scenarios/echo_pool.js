// scenarios/echo_pool.js - proves the pool flow against the echobot.
// Run twice:
//
//   1. ./k6 run scenarios/echo_pool.js                # registers users
//   2. ./k6 run scenarios/echo_pool_replay.js         # reuses them
//
// This run does the "register" half: each VU generates a chat ID, sends
// /start to the echobot, awaits the reply, then appends a user record
// tagged "registered" to data/echo-users.ndjson.

import tg from 'k6/x/telegym';
import { check } from 'k6';

export const options = {
  scenarios: {
    register: {
      executor: 'shared-iterations',
      vus: 20,
      iterations: 100,
      maxDuration: '30s',
    },
  },
  thresholds: {
    checks: ['rate>0.99'],
  },
};

const POOL_FILE = './data/echo-users.ndjson';

export default function () {
  const u = tg.newUser(0);
  u.send('/start');
  const reply = u.awaitText('^Hello', 5);
  const ok = check(reply, {
    'welcome arrived': (m) => m !== null,
    'has echo button': (m) => m && m.findButton('echo_btn') !== null,
  });
  if (!ok) return;

  tg.appendUser(POOL_FILE, u, {
    tags: ['registered'],
    attrs: { source: 'echo_pool' },
  });
}
