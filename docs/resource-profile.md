# Resource profile

Rixa's development profile distinguishes **configured ceilings**, **warm reserve**, **observed demand**, and **capacity claims**. They are not interchangeable.

## Configuration

The top-level `resources` object passes typed limits into the released AChrix Identity, Audit and Media constructors and owns one Rixa-specific Editorial admission limit. Zero preserves the selected AChrix v0.3.0 defaults rather than meaning unlimited.

| Owner | Setting | Zero/default | Explicit minimum |
| --- | --- | ---: | ---: |
| AChrix Identity | `max_conns` | 4 | 1 |
| AChrix Identity | `max_operations` | 16 | 2 |
| AChrix Audit | `max_conns` | 4 | 1 |
| AChrix Audit | `max_operations` | 16 | 2 |
| AChrix Media | `max_conns` | 4 | 1 |
| AChrix Media | `max_operations` | 4 | 1 |
| Rixa Editorial | `editorial_max_operations` | 4 | 1 |

Positive operation values are additionally bounded by Rixa's 32-bit configuration representation limit. That limit is input safety, **not** a supported host capacity. AChrix Media's separate expensive-image budget remains Foundation-owned and is not widened by these settings.

Rixa does not override the selected AChrix pool lifecycle: `MinConns=0`, `MinIdleConns=0`, demand-created connections, one-minute idle/health behavior and bounded connect/ping behavior remain in force. Editorial does not keep a pool; each admitted operation opens one bounded direct PostgreSQL connection and closes it on release.

Static publication reads remain filesystem/manifest based and have a fixed admission ceiling of 32 per publication-enabled site. Publication mutation remains serialized one-at-a-time per publication-enabled site. These are different units from database connections and AChrix Module operation leases and must not be added as though they were interchangeable.

## Aggregate budget

For one process replica with `S` enabled sites and `P` publication-enabled sites:

- control DB maximum = Identity connections + Audit connections;
- per-site DB maximum = Identity + Audit + Media connections + Editorial admission;
- runtime DB maximum = control maximum + `S * per-site maximum`;
- runtime DB warm reserve = **0**;
- AChrix operation-lease maximum = control Identity+Audit leases + `S * (Identity+Audit+Media leases)`;
- Editorial operation maximum = `S * editorial_max_operations`;
- static public-read admission = `P * 32`;
- publication-apply admission = `P * 1`.

`Config.ResourceBudget(replicas)` calculates the same finite plan and multiplies each resource unit separately by an explicit deployment replica count; DB connections, Module leases, Editorial admission and public-read/publication admission remain separate fields and are never added into one meaningless total. Rixa itself does not create or coordinate replicas, and the multiplier does not make shared-`public_root` multi-writer publication supported; each counted replica still has to satisfy the publication topology contract. Migration, preflight, bootstrap and operator/measurement connections are transient processes/connections outside the steady-state runtime maximum and must be budgeted separately when operators overlap them intentionally.

With all module values left at their selected v0.3.0 defaults, two active publication-enabled sites have a one-replica steady-state DB ceiling of **40**: 32 AChrix pool connections plus 8 Editorial direct-operation connections. The corresponding AChrix operation-lease ceiling is 104; Editorial admission is 8, public reads 64 and publication mutation 2. These are ceilings, not expected demand.

Disabled sites are not constructed and contribute zero runtime DB/operation/publication budget. An inventory with exactly one declared site does not compose AChrix Multi-Site; a declared multi-site inventory may retain disabled bindings in the resolver, but those disabled sites still acquire no site runtime resources. The Multi-Site resolver itself owns no Rixa PostgreSQL pool.

## Demand and fairness proof

The opt-in repository-owned `TestRuntimeResourceBoundProfile` uses real PostgreSQL 18.6 and a two-active-site plus one-disabled-site Rixa composition. Runtime CI executes it once in the dedicated **Measure resource profile** step rather than duplicating the same observation inside the broad race suite.

The measurement profile deliberately raises each AChrix pool ceiling to 6 while keeping each Module operation admission at 2 and Rixa Editorial at 2. Its one-replica DB ceiling is 52 with warm reserve 0. The proof verifies:

1. migration/bootstrap have drained and construction alone leaves zero target-database sessions;
2. startup demand is nonzero but remains below the configured maximum, proving that a higher maximum is not eagerly allocated;
3. two site-A Identity reads are held behind a task-owned PostgreSQL table lock, while a third fails immediately with `identity.ErrLimited`;
4. while those management reads remain blocked, eight concurrent GETs to a real empty site-B publication generation are served successfully from the native static path and the observed count of active PostgreSQL sessions does not increase;
5. the equivalent site-B Identity read still succeeds under a bounded deadline, proving that site A cannot consume site B's independent pool/admission budget;
6. after the lock is released, admitted work completes, active DB demand returns to zero and admission is reusable; idle pool connections may remain until native health reclamation and no immediate RSS/physical-connection drop is promised;
7. graceful runtime shutdown returns all target-database session counts to zero.

The exact constructor/start/saturation/mixed-load/post-burst counts are observation evidence tied to the current CI run and its disposable GitHub-hosted runner, not durable capacity numbers. The test logs `runner_goos`, `runner_goarch`, logical CPU count and Linux `MemTotal` together with the workload counts so the measured host profile is declared by evidence rather than assumed from a runner label. Record those values in the PR/CI evidence for the exact candidate. Do not turn them into a production SLO.

Existing lifecycle regression proof remains authoritative for partial startup cleanup, and publication shutdown tests retain the rule that a failed publication drain prevents dependent Application teardown from racing active publication work.

## Supported claim boundary

This slice establishes a measured **development** profile, finite aggregate planning math, demand-based construction, per-site isolation and bounded admission. It does not establish:

- a 20-site, 1000-site or fleet-size guarantee;
- production throughput, latency, RSS or database-sizing targets;
- cross-replica fairness or a shared scheduler;
- an adaptive resource manager;
- transfer/restore concurrency (owned by the next Issue #6 slice);
- production HA/CDN behavior.

Tune only from representative workload evidence. Increasing a maximum permits more concurrent resource use; it does not promise that the host, PostgreSQL server or application will benefit.
