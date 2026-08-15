# Findings

- Maintained `master` is clean at `90ae75df` and matches the live production image tag `cli-proxy-api:refill-heartbeat-90ae75df-amd64`.
- The feature branch is clean at `8d1bb6f0`, based on current `origin/main` at `78f0c407`.
- `master` and the feature branch share upstream `v7.2.131` commit `323b7276`; merging brings 13 upstream fixes plus the nine feature commits while preserving 30 production-master commits.
- Production host is `174.128.243.42`, Compose workdir `/data/apps/cli-proxy-api`, file `docker-compose.prod.yml`.
- Live container is running with restart=0, OOM=false, root HTTP 200, unauthenticated models 401, about 3.0 GiB memory at baseline.
- Live plugin settings currently have author `weekly_overdraft_enabled=true` and adaptive variants disabled; the new core config is not present.
