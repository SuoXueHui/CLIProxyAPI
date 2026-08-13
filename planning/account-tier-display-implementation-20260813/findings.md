# 发现记录

- 根因与方案沿用 `planning/cpa-xai-tier-display-20260813/` 的已完成只读排查。
- 当前 CLIProxyAPI 功能 worktree：`/Users/suo/.config/superpowers/worktrees/CLIProxyAPI/account-tier-display-20260813`，分支 `codex/account-tier-display-20260813`，基线 `fork/master=2f034843`。
- 当前 Manager Plus 功能 worktree：`/Users/suo/.config/superpowers/worktrees/CPA-Manager-Plus/account-tier-display-20260813`，分支 `codex/account-tier-display-20260813`，基线为 fork 当前 master。
- 生产历史基线仅供定位：主机 `174.128.243.42`、CPA 目录 `/data/apps/cli-proxy-api`；发布前将重新核对 live runtime。
- 生产最终镜像：`cli-proxy-api:master-v7.2.130-account-tier-66e2f6bc`、`cpa-manager-plus:account-tier-7cf0686a-amd64`；代码 fork master 分别为 `cbd33884` 与 `02137eaf`。
- xAI 线上 5 个认证只有 3 个 token 带可识别 tier；界面只对这 3 个显示 Heavy，其余 2 个保持未知是预期安全行为，不代表 Free。
- Codex 线上 228 个认证均已有 `id_token.plan_type`，因此本次 Manager 独立 badge 可全部展示；等级分布为 Free 25、Team 203。
- Manager 生产随后切换到 `cpa-manager-plus:usage-window-0b3fa181-amd64`；该提交以套餐功能 `7cf0686a` 为祖先并保留套餐 badge，同时增加另一任务的本地窗口时间进度。
- Manager fork 最终合并 `master=2cf66b31`，同时包含套餐功能、内嵌页面与 usage-window 最终变更；生产镜像仍是其已验证祖先链产物。
