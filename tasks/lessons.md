# Lessons

- Exercise bootstrap changes first with a late-joining node on a seeded private
  network. Reserve multi-hour public Testnet runs for final scale validation,
  not the primary implementation loop.
- A changing acquisition frontier and bounded steady-state heap do not prove the
  terminal completeness path is bounded. Test acquisition, `FinishSync`, ledger
  adoption, and post-adoption consensus as separate acceptance milestones.
- Check protocol object limits before choosing a synthetic state generator.
  `TicketCreate` caps an account at 250 outstanding tickets, so scale Ticket
  state across independently funded deterministic accounts instead of repeatedly
  creating tickets on one account.
- When a user says to implement a ranked optimization list, preserve the stated
  scope exactly. Do not silently narrow it to the first recommendation; restate
  any ambiguity before taking action.
- Respect repository command constraints even during quick diagnostics. Use
  `rg` context and output limits instead of `head`, and issue independent shell
  commands as separate tool calls instead of chaining them with separators.
