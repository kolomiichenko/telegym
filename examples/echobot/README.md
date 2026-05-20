# echobot example

A minimal Telegram bot that echoes back whatever you send, plus a handful of
k6 scenarios that drive it. Used as a self-contained demo to validate the
telegym stack end-to-end without bringing your own bot.

## What's here

```
main.go              # the bot itself (~150 LOC, stdlib + http only)
run.sh               # convenience runner: spawns mock+bot, executes k6 scenario
scenarios/
  echo.js            # send /start, expect "welcome" reply within 500ms
  echo_pool.js       # register N users, append them to a NDJSON pool
  echo_pool_replay.js  # read the pool back and replay each user
  _lib/pool.js       # shared SharedArray + parsePool helper
data/                # NDJSON pools written by echo_pool.js
```

## Quickstart

From the repo root:

```bash
make build              # produces ../../bin/{telegym-mock,echobot,k6}
./examples/echobot/run.sh                # default: echo scenario
./examples/echobot/run.sh echo_pool
./examples/echobot/run.sh echo_pool_replay
```

`run.sh` launches `telegym-mock` on `:5678` and `echobot` on `:8443`, points
the bot at the mock via `TELEGYM_MOCK_URL`, then invokes the k6 binary on
the chosen scenario. Both processes are killed on exit.

## Manual run (without run.sh)

```bash
# Terminal 1
./bin/telegym-mock -quiet

# Terminal 2
./bin/echobot

# Terminal 3
TELEGYM_BOT_TOKEN=1234567890:telegym_default_mock_token_xxxxxxxx \
  ./bin/k6 run examples/echobot/scenarios/echo.js
```

## Writing your own scenario

`echo.js` is the smallest possible scenario and is a good copy-paste base:

```js
import tg from 'k6/x/telegym';
import { check } from 'k6';

export default function () {
  const u = tg.newUser(0);
  u.send('/start');
  const m = u.awaitText('^welcome', 5);
  check(m, { 'welcome arrived': (r) => r !== null });
}
```

See the top-level README for the full xk6-telegym scenario API.
