# Security Policy

## Reporting a vulnerability

Please report security issues **privately** by emailing
[i@andrii.pro](mailto:i@andrii.pro) instead of opening a public GitHub
issue.

Include:

- A description of the issue and the impact
- Steps to reproduce (or a proof-of-concept)
- Affected version / commit (output of `git rev-parse HEAD` if you built
  from source)
- Any suggested mitigation, if you have one

You should receive an acknowledgement within 72 hours. A fix or
mitigation will be coordinated with you before any public disclosure.

## Scope

In scope:

- The mock server (`telegym-mock`) - HTTP endpoint handlers, the spec-
  driven generic dispatcher, the debug-chat UI, the file store
- The proxy (`telegym-proxy`) - inbound update handling, file relay,
  token usage, user-id allowlisting
- The k6 extension (`pkg/xk6`) - anything reachable from a scenario
- Embedded third-party assets (htmx, idiomorph) - we'll forward
  upstream-affecting reports to the maintainers and bump our pinned
  version

Out of scope:

- Bugs in your own bot that telegym surfaces during load testing
- Vulnerabilities in the real Telegram Bot API (report to Telegram)
- Missing rate-limiting on the mock - by design, this is a load-test
  target, not a production server
- Findings that require an attacker already on the same machine /
  network as the mock (the mock binds to localhost by default and is
  not intended to face the internet)

## Versions

Security fixes land on `main` and are tagged in the next release. There
is no separate long-term support branch.
