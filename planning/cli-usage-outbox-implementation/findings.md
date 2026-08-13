# Findings

- The current implementation broadcasts usage directly to subscribers and skips the in-memory queue when any subscriber accepts the payload.
- The legacy management endpoint calls `PopOldest`, so records are removed before a downstream database commit.
- Queue retention defaults to 60 seconds and is memory-only.
- The root module did not yet declare `modernc.org/sqlite`; the task owner explicitly approved adding it.
- Default durable path must be resolved from the active config file as `<config-dir>/usage-outbox.sqlite`; only the literal `disabled` selects legacy memory mode.
- A process-global queue is shared by server test instances, so repeated same-path configuration must be idempotent and must not close the active SQLite handle.
- Subscriber notifications are now a best-effort fast path only; the durable insert occurs before notification, and slow subscriber removal cannot delete the record.
- The legacy pop API intentionally remains destructive by internally claim-and-acking the selected durable records.
- Control frames (`support_refresh` and `refresh`) only use subscriber delivery and are excluded from the outbox.
- The claim/ack HTTP contract uses `lease_id`, stable `delivery_id`, and nested raw `payload` JSON.
