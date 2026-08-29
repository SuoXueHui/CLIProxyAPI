# CPA Adaptive Scheduler

## Goal

Keep ingress concurrency unchanged while preventing new requests from piling onto slow OAuth auth entries. Add in-memory soft avoidance and non-blocking fair load selection, covering built-in and plugin scheduler paths.

## Steps

- [completed] Add routing configuration, defaults, validation, and runtime propagation.
- [completed] Add failing tests for soft penalty, fail-open behavior, and fair load selection.
- [completed] Implement scheduler runtime state and selection integration.
- [completed] Measure stream first event/total duration and release in-flight leases.
- [completed] Integrate plugin scheduler candidate filtering and observability metadata.
- [completed] Run focused tests, race tests, full tests, and build verification.
- [completed] Commit branch, merge into master, and deploy with post-deploy checks.
