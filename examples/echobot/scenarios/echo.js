// scenarios/echo.js - smoke test for the telegym stack.
//
// Run prerequisites in three terminals:
//   1. ./bin/telegym-mock                                   (mock Bot API on :5678)
//   2. ./bin/echobot                                  (echo bot on :8443)
//   3. ./k6 run scenarios/echo.js                     (xk6-built k6 binary)
//
// The bot under test here is cmd/echobot/main.go - replace with your real bot
// for actual load runs.

import tg from 'k6/x/telegym';
import { check, sleep } from 'k6';

export const options = {
  scenarios: {
    echo: {
      executor: 'ramping-vus',
      stages: [
        { duration: '5s',  target: 10 },
        { duration: '30s', target: 50 },
        { duration: '5s',  target: 0  },
      ],
      gracefulRampDown: '5s',
    },
  },
  thresholds: {
    checks: ['rate>0.99'],
  },
};

export default function () {
  const u = tg.newUser(0);

  // 1. Send /start, await welcome message containing the bot's "Hello" reply.
  u.send('/start');
  const hello = u.awaitText('^Hello', 5);
  check(hello, {
    'welcome arrived':       (m) => m !== null,
    'welcome under 500ms':   (m) => m.latencyMs < 500,
    'has echo button':       (m) => m.findButton('echo_btn') !== null,
  });

  // 2. Click the inline button, await acknowledgement.
  u.click('echo_btn');
  const ack = u.awaitText('you clicked', 5);
  check(ack, {
    'click ack arrived':     (m) => m !== null,
    'click ack under 500ms': (m) => m.latencyMs < 500,
  });

  sleep(0.5);
}
