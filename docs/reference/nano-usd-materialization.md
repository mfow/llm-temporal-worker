# Conservative nano-USD materialization

The durable ledger and public Go/JSON/OCaml contracts represent money as exact
fixed-scale USD (`NUMERIC(38,18)`). Redis is only a checked admission
materialization and stores non-negative integer `NanoUSD` values. The conversion
contract is versioned as `nano_usd_v1`.

- A positive charge or reservation uses `pricing.CeilNanoUSD`: `ceil(usd × 10^9)`.
- A configured budget limit uses `pricing.FloorNanoUSD`: `floor(usd × 10^9)`.
- Redis values must be at most `2^53 - 1` (`9,007,199,254,740,991` nano-USD),
  so a limit above `9,007,199.254740991` USD is rejected.
- Finalize/release must subtract the exact integer stored for the reservation;
  it must not recompute a rounded value from the current USD total.

All arithmetic is integer/big-integer arithmetic; no floating-point conversion
is permitted. Therefore a materialized charge is never below the exact charge,
and a materialized limit is never above the exact limit. The discarded
fraction can conservatively over-throttle by less than one nano-dollar per
positive event. `USDFromNano` is only a display/reconciliation conversion and
does not recover fractions discarded at the materialization boundary.
