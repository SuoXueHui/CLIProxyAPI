# CPA 账号等级显示实现与发布计划

## 目标

在不暴露认证 Token 的前提下，为 CPA Manager Plus 的 Codex 与 xAI 认证卡片显示独立套餐等级；完成 CLIProxyAPI 与 Manager Plus 分支修改、验证、合并 fork `master` 并按现有可回滚流程发布线上。

## 已确认设计

- Codex：复用 auth-file 响应中已有的 `id_token.plan_type`，将套餐展示移出额度成功态。
- xAI：CLIProxyAPI 仅从显式 metadata 或已加载 access-token JWT 的非敏感 `tier` claim 输出标准化 `xai_plan_type`/`xai_plan_source`；不返回 Token，不凭 `$0/$0` 判断 Free。
- Manager：统一在卡片级展示套餐 badge；未知证据显式显示 Unknown，额度和错误状态机保持不变。

## 阶段

1. [completed] 复核现场根因、仓库边界与用户批准的实现方向。
2. [in_progress] 完成 CLIProxyAPI xAI 套餐证据接口与回归测试。
3. [in_progress] 完成 CPA Manager Plus 独立套餐 badge 与前端测试。
4. [pending] 交叉 review、全量验证并提交两个功能分支。
5. [pending] 合并到各 fork `master`，推送远端并生成可回滚发布产物。
6. [pending] 只读核对生产后备份、灰度/切换服务端与 Manager，验证页面和 API。
7. [pending] 更新 planning、AGENTS baseline（如插件基线变化）与 Obsidian 变更记录。

## 风险与边界

- 当前主工作区有无关未提交修改，不直接在其上合并；使用隔离 worktree。
- xAI 刷新后的 Token 可能不含 tier，此时不得误标 Free。
- 生产改动前必须现场确认 mounts、运行镜像、restart/OOM 与回滚路径。
- 所有输出与 planning 不记录管理密钥、OAuth Token 或原始认证 JSON。
