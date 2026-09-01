# Codex 高并发 429 假设验证计划

## 目标

实现并验证“一个物理 Codex 账号展开为多个运行时分身、每个分身绑定独立静态 IPv6、每个分身具有独立并发上限”的账号级能力，同时保证默认关闭、管理页面仍只显示一个账号、历史用量继续聚合到物理账号。

## 已确认设计

- 物理 auth JSON 使用 `codex_replica: {enabled, count, concurrency}`；未配置或 `enabled=false` 时保持现有单 auth 行为。
- `count` 表示总运行时分身数，不额外保留第七个“原始”运行时 auth；默认 6。
- `concurrency` 表示每个运行时分身的硬并发上限；默认 10。
- 分身拥有独立运行时 auth ID、IPv6、设备身份、冷却和负载状态，但共享物理文件名及稳定 `auth_index`。
- CPAMP 依据共享物理身份聚合为一行，展示分身数、单分身并发、总并发和当前活跃并发；usage/history 继续按共享 `auth_index` 聚合。
- 配置入口放在截图对应的 CPAMP 凭证管理账号配置抽屉，只对 Codex 物理账号显示。

## 阶段

- [completed] 1. 只读检查当前 CPA 配置、auth 池、IPv6 和近期 429 基线
- [completed] 2. 核对现有能力：按 auth ID 的 IPv6 分配、账号并发限制、Codex 设备身份与会话黏性
- [completed] 3. 用户确认方案，并补充账号级配置与列表聚合 UI 要求
- [completed] 4. 以 TDD 实现 CPA 运行时分身展开、并发 admission 和管理投影
- [completed] 5. 以 TDD 实现 CPAMP 配置表单、列表聚合及并发展示
- [completed] 6. 完成核心、Manager 前后端测试、构建与静态页面验证
- [completed] 7. 构建隔离候选镜像并执行阶梯测试；20 并发触发停止条件，未继续 40/60
- [completed] 8. 对比 429、成功率、延迟、实际 auth/IP 分布和运行时投影
- [completed] 9. 根据结果决定不切换生产，保留无凭据构建证据并清理候选凭据
- [completed] 10. 等待现有 zero-impact release 完成，冻结新的生产与 `master` 基线
- [in_progress] 11. 基于冻结基线复核交叉改动、执行全量回归并提交 Core/Manager
- [pending] 12. 推送两个仓库 `master`，构建带明确 commit 的正式候选镜像
- [pending] 13. 串行发布 Core 后发布 Manager；Core 使用 quiesce/ownership transfer，Manager 不参与 API 流量切换
- [pending] 14. 完成生产验收、观察窗口、回滚材料与知识文档收口

## 安全边界

- 生产配置、auth、正式容器和数据库未修改；所有写入均位于独立候选目录、镜像和候选容器。
- 原始 OAuth JSON 只作为输入，不在报告、日志或 planning 文件中写入 token、邮箱、管理密钥或完整账号 ID。
- 分身只存在于运行时，不复制 OAuth JSON；物理 auth 文件仍保持一个，避免 refresh token 分叉和多文件配置漂移。
- 每个运行时分身使用唯一 auth ID，静态地址必须来自现有 IPv6 前缀且不能复用现网地址。
- 若只能复制同一个 OAuth token，而不能获得 6 个独立且有效的上游 credential/session，实验只能证明 CPA 分流效果，不能证明上游账号额度增加。
- 任何 429 比例上升、401/402、bind/route 错误、容器重启/OOM 或候选无法隔离时立即停止。
- 不与正在进行的发布并行切换线上容器；必须等当前任务明确完成 ownership transfer、Compose 和网络别名收口后再进入本功能发布。
- 正式发布只提交本任务文件，排除其他未跟踪 planning 目录；推送前再次确认远端 `master` 未产生新提交。
