# Findings

- The refill controller requires a usage watermark within two minutes.
- Production has no schedulable Codex credential, so model requests return 503 and no usage event can refresh the watermark.
- RESP PONG is transport-only evidence. An explicit queue-enabled control frame is stronger and does not pollute usage accounting.
- The selected frame is `{"heartbeat":true}` every 30 seconds on the existing authenticated `usage` subscription.
