# 发现记录

## 当前运行态（2026-09-01，174.128.243.42）

- CPA 容器为 `cli-proxy-api-member-usage`，运行约 36 小时，`Restart=0`、`OOM=false`；Manager 与补货控制器也处于 healthy/无重启状态。
- 当前 auth 目录有 34 个 JSON，其中 21 个 Codex；Manager 当前 Codex 账号并发限制全部为 0，即不限制。
- CPA 已启用 `ipv6-egress`，模式为 `auto`，使用 `2610:150:805f:f80e:100::/80` 前缀；容器 `eth1` 上已经存在大量该前缀下的 auth 级 IPv6 地址。
- 已启用 `routing.adaptive-auth`、Codex `identity-confuse` 和 `account-device-identity: account_device`；因此按不同 auth ID 建立的候选副本会获得独立的 CPA 调度键、IPv6 地址和安装身份。
- 当前全局 `request-retry=1`、`max-retry-credentials=2`、`max-retry-interval=3`，`routing.session-affinity=true`、TTL 1 小时。会话黏性会让同一个 session 长时间固定到一个 auth，可能造成局部热点。
- 最近 5 分钟 Codex 429 主体为 `{"detail":"Rate limit exceeded"}`。最近 5 分钟的 Codex 事件主要集中在 5 个 auth，单 auth 约 347-514 次请求、约 170-418 次失败，说明现网分布并不均匀。
- 最近 15 分钟 Codex 涉及 6 个 auth，429 主体仍以 `Rate limit exceeded` 为主；另有少量 400/404/408/502，压测请求必须固定为合法、短请求，避免把输入错误和上游过载混入结论。
- 最近 24 小时 usage 数据同时出现 `usage_limit_reached`、token invalidated 和 `server_is_overloaded`，因此不能只看 HTTP 429 总数，必须按错误摘要和响应头拆分。

## 已核对的代码语义

- `internal/egress/ipv6.go` 的 allocator/controller 以 auth ID 为稳定键；复制文件并产生不同 auth ID 后会分配不同地址，但地址所有权仅在当前 network namespace/容器内成立。
- Manager `AccountConcurrencyService` 以 `AuthID` 为键，限额 10 是 CPA 本地 admission 上限；达到上限时会直接返回 `account_concurrency_limit_reached`，这类 429 不能算上游 429。
- Codex `account-device` 安装 ID 由 auth ID 稳定派生；这会让不同 auth 副本拥有不同安装身份，但不会改变上游按账号/工作区聚合的 quota 语义。
- `routing.session-affinity` 在冷绑定后会优先复用原 auth；canary 必须关闭会话黏性，或为每个 worker 使用不同且稳定的 session ID，否则无法验证 6 个分身是否真正均匀承载流量。

## 假设与判定

1. 若 6 个独立 OAuth credential/member 在 6 个 IP 上运行，429 随每个分身均匀分布且总体错误率明显下降，说明分身/出口分散有效。
2. 若复制同一个 token 后仍出现同一 `usage_limit_reached` 或 429 率不降，说明上游按账号/工作区或 token 级聚合，增加文件数和 IP 无法增加额度。
3. 若关闭 session affinity 后显著改善，而只设置 6×10 不改善，主因是现网会话热点，不是 IP 数量。
4. 若 CPA admission 429 增加但上游 429 下降，说明 10 并发限额在保护上游；需按用户体验决定是否接受排队/快速失败。

## 2026-09-01 用户确认后的实现判断

- 截图页面实际来自 `CPA-Manager-Plus` 的 `management.html#/accounts`，不是 CPA-A Manager 插件页面；UI 改动必须落在 `/Users/suo/work/project/CPA-Manager-Plus-ipv6-detail-prod`。
- CPA 已支持一个物理 auth 文件展开为多个插件虚拟 auth 的通用契约，且 CPAMP 已有同物理来源的安全修改回退；本功能复用该契约，不复制 OAuth 文件。
- 所有分身显式继承同一个稳定 `auth_index`，可让现有 usage 事件和 CPAMP account-history 聚合保持一个逻辑账号，同时用不同运行时 auth ID 驱动独立调度、IPv6 和设备身份。
- 单分身并发不能只保存在 UI。CPA 核心需要在选中 auth 后原子获取运行时槽位，饱和时继续尝试同组其他分身；全部饱和才返回可区分的本地并发错误。
- OAuth 自动刷新与 401 同步刷新必须按物理账号串行，不能让 6 个分身同时刷新同一 refresh token。只有 leader 分身参与定时刷新，按共享分组键串行同步刷新。

## 2026-09-01 实现证据

- `codex_replica` 已形成核心与 CPAMP 共享契约，默认关闭，开启默认值为 6 个分身、每个分身 10 并发；核心强制范围为分身 1-64、单分身并发 1-1000。
- 内置文件解析和生产实际使用的插件解析两条路径都执行分身展开；插件路径若遗漏会出现“本地测试通过但生产不展开”的假阳性，现已用独立测试覆盖。
- 内置调度器和会话黏性选择器都会实时跳过已满分身；选中后发生并发竞争时，本地 admission 错误不计入上游凭据失败和 `max-retry-credentials`，会继续轮转其他分身。
- 60 并发阻塞测试确认 6 个分身可以同时各持有 10 个不同请求，分布为 10/10/10/10/10/10；流式请求保持槽位直到终止 chunk 或上下文取消。
- 核心管理列表将同组运行时分身投影为一个物理账号，并聚合成功/失败、近期请求、活跃并发和 IPv6 分配数；共享 `auth_index` 让 CPAMP 历史用量仍落在同一账号行。
- CPAMP 配置抽屉仅对 Codex 显示分身开关，关闭时隐藏数量输入；开启后显示分身数、单分身并发和总容量。账号列表显示 `分身 N × C` 与 `并发 active / total`。
- 核心 PATCH 接口在持久化前验证嵌套配置，避免绕过 UI 写入非法值导致 watcher 跳过认证文件。

## 2026-09-01 候选实测结论

- 最终 Core 候选镜像为 `cli-proxy-api:v7.2.147-codex-replica-canary3-20260901-amd64`，Manager 候选镜像为 `cpa-manager-plus:v1.12.6-codex-replica-canary-20260901-amd64`；均为 Linux amd64，Core 动态链接 libc 并成功加载作者插件 v0.3.1356。
- 候选管理投影始终只有一行物理账号，初始为 6 个分身、单分身并发 10、总容量 60、IPv6 6/6。容器内全局 IPv6 数为 7，包括 1 个容器主地址和 6 个分身地址。
- 6 并发低压验证 6/6 成功，峰值 active=6；逐运行时 ID 查询确认分身 1-6 均为 success=1、failed=0、egress_assigned=1，证明六个分身同时处理不同请求。
- 10 并发为 10/10 HTTP 200、无本地或上游 429，P50 约 1.99 秒，P95 约 12.87 秒。20 并发为 16/20 HTTP 200、4/20 上游 `Rate limit exceeded`，本地分身限流为 0，P50 约 1.98 秒、P95 约 5.78 秒。
- 20 并发已达到预设停止条件，因此没有继续 40/60。最终单请求仍为 HTTP 200，但尾延迟约 24.3 秒，进一步表明上游账号侧压力未因多 IP 消失。
- CPAMP 代理实际执行 `6×10 -> 2×3 -> 6×10` 均返回 200，不重启即可完成拓扑和 IPv6 变更；每一步账号仍为一行。恢复后 6 个分身各注册 10 个模型，`gpt-5.4` 可用。
- 原始 auth SHA256 前后相同。结论是不发布当前候选：分身能力本身有效，但“同一账号稳定获得 60 并发”的假设不成立。

## 2026-09-01 正式发布前网络复核

- 最终提交镜像在独立合成 auth 上再次验证为一行、6 个分身、单分身并发 10、总容量 60、IPv6 6/6，作者插件 v0.3.1356 已加载注册，未使用真实 OAuth。
- 发现现有 `cpa-v6-default` Docker bridge 存在 IPv6 `MASQUERADE`：容器内虽然有独立 auth IPv6，但外部回显为主机主 IPv6，因此不能把“容器内绑定成功”当成“上游看到独立 IP”。
- 关闭 bridge masquerade 后，该 VPS 因缺少可用的路由/NDP 路径而无法直连；静态 proxy NDP 验证仍超时。
- `macvlan` 直连验证成功：容器静态 IPv6 和运行时分身 IPv6 的外部回显均与源地址完全一致。正式发布需要把账号 IPv6 网络迁移到 macvlan，并把外部源地址一致性作为验收门禁。

## 2026-09-01 切换失败与单候选重试约束

- 第一轮 Router -> 最终候选只读切换期间，候选错误主体仍为上游 `Rate limit exceeded`/`server_is_overloaded`；同窗口旧实例错误率更高或同级，未证明候选代码回归。
- 旧 Core 有 132 个在途请求；直接等待 quiescing 归零会造成新请求排队。第二轮改用 buffer 后，三套 CPA 同时加载完整 auth/model 状态，主机 SSH banner 与公网服务出现间歇性超时，触发资源安全门禁。
- 回滚后旧 Core 已恢复 `active`/writer/IPv6/plugin，Router 已恢复 `cpa-green`；Manager 仍使用线上原镜像，生产功能未切换。
- 主机内存不能扩容时，后续只能保留一个候选，候选最大内存上限不得与旧实例叠加超出现场余量；必须取消 buffer，且切换安排在低流量窗口。

## 2026-09-01 单候选正式发布结果

- Core 正式镜像为 `cli-proxy-api:v7.2.147-codex-replica-84c73127-amd64`，Manager 正式镜像为 `cpa-manager-plus:v1.12.6-codex-replica-20cb2f80-amd64`；两者均由 Compose 管理，canonical Core Compose 已更新到 `/data/apps/cli-proxy-api/docker-compose.prod.yml`。
- 首次单候选激活失败的直接原因不是业务逻辑，而是候选进程启动时读取了旧 `eth0`/`100::/80` egress 配置；运行期间仅改绑定文件不能替换已建立的进程内 controller。以 `eth1`、`103::/80` 从启动时重建同一候选后激活成功。
- Router 的 proxy/management upstream 最终均为 `cpa-replica-single`；旧 Core 在 outbox `pending=0/inflight=0` 后降为 standby 并停止，保留旧镜像、Compose、数据和日志作为回滚资产。
- 新 Core 生命周期为 `active`，writer lease、auto refresh、IPv6、plugin runtime 全部启用；49 个 auth、26 个模型、作者插件 v0.3.1356 注册成功，usage durable 队列持续清零且 produced/acked 同步增长。
- macvlan 容器内有 1 个容器地址和 49 个当前物理 auth 地址；所有地址均为 `nodad` 且无 tentative/dadfailed。选取一个 auth IPv6 做容器 network namespace 出站，外部回显与指定源地址完全一致。
- 当前生产 auth 默认均未配置 `codex_replica`，因此发布不会自动改变账号拓扑。浏览器实测配置抽屉默认关闭；临时打开未保存时显示分身数 6、单分身并发 10、总容量 60，随后已恢复关闭并退出抽屉。
- 最终稳定窗口中公网、直连和 Manager health 均为 HTTP 200，Core/Manager/Router restart=0、OOM=false。观察到的一组 16 个 503 来自 `grok-imagine-image` 错误调用 `/v1/chat/completions`，与 Codex 分身发布无关；窗口内没有 Core 429。
