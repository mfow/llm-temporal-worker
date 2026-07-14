# LLM Temporal Worker

This repository contains the contract, implementation foundation, and staged
plans for a reusable Go inference library and Temporal Activity Worker. The
implemented foundation covers the semantic request/response API, strict
configuration snapshots, official-SDK provider adapters, fragmented stream
normalization, deterministic routing, tenant-bound continuation handles, exact
pricing, and in-memory admission. The process runtime composition now includes
reloadable snapshots, probe servers, graceful worker shutdown, and TLS-safe
Temporal client wiring. The provider/state-backed `EngineFactory` and
production shared-state backends remain explicit seams described by the plans;
the CLI fails closed until those deployment-specific dependencies are supplied.

Start with the [documentation index](docs/index.md), then follow the
[master implementation sequence](docs/superpowers/plans/2026-07-13-master-sequence.md).
