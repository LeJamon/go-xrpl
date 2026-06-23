# Security Policy

go-xrpl is an idiomatic Go implementation of an [XRP Ledger](https://xrpl.org/)
node. It is actively developed, built in public, and has **not** been
independently audited. Do not rely on it to safeguard mainnet funds. We
nonetheless take security seriously and appreciate reports that help us harden
the node.

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
pull requests, or discussions.**

Instead, report them privately through GitHub's
[private vulnerability reporting](https://github.com/LeJamon/go-xrpl/security/advisories/new):

> Go to the repository's **Security** tab → **Report a vulnerability**, or open
> <https://github.com/LeJamon/go-xrpl/security/advisories/new> directly.

This opens a private security advisory visible only to you and the maintainers,
where we can discuss and fix the issue before it is publicly disclosed.

To help us triage quickly, please include as much of the following as you can:

- A description of the vulnerability and its impact (e.g. consensus divergence,
  fund loss, denial of service, state corruption, panic/crash).
- The affected commit, branch, or version.
- Step-by-step reproduction instructions, ideally a minimal test case or the
  transaction/ledger inputs that trigger it.
- Any proof-of-concept code, and your assessment of severity.

## What to Expect

- We aim to acknowledge new reports within **5 business days**.
- We will work with you to understand and validate the issue, keep you informed
  of progress toward a fix, and coordinate a disclosure timeline.
- With your permission, we are happy to credit you for the discovery once a fix
  is released. If you prefer to remain anonymous, let us know.

We follow coordinated disclosure: please give us a reasonable opportunity to
release a fix before disclosing the issue publicly.

## Supported Versions

go-xrpl is pre-1.0 and has no stable releases yet; development happens on the
`main` branch. We accept reports for vulnerabilities present in:

- the current `main` branch, and
- any [open pull requests](https://github.com/LeJamon/go-xrpl/pulls).

## Responsible Investigation

Because go-xrpl participates in a live financial network, please investigate
responsibly:

- **Do not test against XRPL Mainnet.** Use the
  [Testnet or Devnet](https://xrpl.org/xrp-testnet-faucet.html), or a local
  standalone node.
- Do not run denial-of-service attacks, spam, or load tests against public
  XRPL infrastructure or third-party peers.
- Do not attempt to access, modify, or destroy data that does not belong to you,
  and do not use social engineering or target physical infrastructure.
- Make a good-faith effort to avoid disruption or harm to the XRP Ledger and the
  broader ecosystem.

## Protocol vs. Implementation Issues

go-xrpl implements the XRP Ledger protocol, for which
[rippled](https://github.com/XRPLF/rippled) is the de facto specification.

- If the issue is a flaw **specific to go-xrpl** (a divergence from rippled, a
  Go-specific bug, a crash, or a vulnerability in this codebase), report it here
  via the process above.
- If the issue is in the **XRP Ledger protocol itself** — affecting rippled and
  the network as a whole — please also report it upstream through the
  [XRPL bug bounty program](https://github.com/XRPLF/rippled/blob/develop/SECURITY.md)
  (`bugs@ripple.com`), as it may impact the entire network.

Thank you for helping keep go-xrpl and the XRP Ledger ecosystem secure.
