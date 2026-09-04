# v0.2.3 核心缺口修复与发布计划

## 意图

在发布 `herdr-gh-checks` v0.2.3 前，基于当前 fork、上游仓库、代码、测试与发布工作流的实际状态，确认项目是否缺少核心能力，修复会让既有核心流程失效或误导用户的问题，补齐持续集成和可复现的测试说明，然后通过 `gh` 把变更提交到规范上游并完成可验证的发布流程。审计结论是产品功能面已经覆盖 PR/CI 查看、逐行注释、跨 PR 审阅、workflow、update/merge 与 sidebar，不需要再增加新的大模块；但已有实现中存在 7 个 P1 正确性/安全性缺口，当前不能直接发布。

当前工作区是 `zhy0216/herdr-gh-checks` fork，规范发行仓库、安装器和 Herdr 市场均指向 `itisbryan/herdr-gh-checks`。当前认证账号对上游只有读取权限，因此本轮默认发布路径为：修复并验证 → 推送 fork → 创建上游 PR → 上游合并后由有权限的维护者打 tag → 验证 Release → 补充受审 checksum allowlist。不会在未确认的情况下把 fork 重新品牌化为独立发行源。

## 目标

- 修复 Linux/跨平台 merge 确认输入，使标准 merge 与 admin merge 都能可靠确认或取消。
- 让 added、deleted、renamed 文件都能进入单文件 diff/注释流程，并把外部命令失败呈现给用户。
- 隔离异步 PR 状态请求，丢弃旧 generation 的响应，保证同时只有一条持续轮询链。
- 按仓库/审阅目标隔离 notes，避免同名分支跨仓库复用及潜在源码注释泄漏。
- 区分成功与失败消息；失败不得显示绿色成功标记。
- 正确处理 GitHub check 的 `cancel`、`skipping` 与未知 bucket；未知状态不得被判定为通过。
- 修复 Windows 跨卷 PATH 判断，保留仓库外的可信工具路径。
- 增加 PR/push CI，并在 tag 发布前执行格式、模块、测试、race、vet、跨平台构建和版本一致性检查。
- 使用当前已修复的精确发布工具链 Go 1.27.1 构建 v0.2.3。
- 提供仓库内 `TESTING.md`，覆盖自动化、手工验收、安全回归、安装器与发布后验收。
- 通过 fork 分支和上游 PR 交付；权限允许时完成 v0.2.3 Release 及 checksum 二阶段收尾。

## 非目标

- 不把 `zhy0216` fork 擅自改造成新的规范发行源，也不改动原作者归属。
- 不在本次引入 GitHub Enterprise 支持；现有安全边界仍只允许 `github.com`。
- 不重写 Bubble Tea UI 或改造为新的 GitHub API 客户端。
- 不承诺未经真实 Windows 主机验证的完整原生 Windows 交互体验；本轮修复可确定的跨卷问题并保留明确测试项。
- 不在没有上游写权限时伪造“已发布到上游”的完成状态。

## 方案

### 1. 输入、审阅文件和消息正确性

- 用 Go 原生输入读取替代 `/bin/sh read -rsn1`，明确区分 Enter、普通选项、EOF 与读取错误；所有界面提示与实际交互一致。
- 单文件审阅将 base/head 两侧分别解析。某一侧不存在时使用受控空临时文件；当前工作树中存在的目标仍必须通过 regular-file/no-symlink 校验。真实注释路径始终通过 `CI_REVIEW_PATH` 传入。
- `tea.ExecProcess` 的回调把退出错误转换为结构化失败消息，避免按键后无反馈。
- 将动作结果建模为带 severity 的消息；成功渲染绿色 `✓`，失败渲染红色 `✗` 并保留经过终端清洗的实际错误。

### 2. 状态机、notes 与 CI 状态

- `fetchMsg` 携带目标 PR number 和单调递增 generation。切换 PR、reload 或主动刷新时使旧 generation 失效；仅被当前 model 接受的响应可以更新状态并安排下一次轮询。
- notes key 至少包含规范化的仓库/工作区身份、分支和 PR 身份，避免跨仓库、跨审阅目标复用。旧的 branch-only 文件不自动导入，优先保证隔离。
- `ciSummary` 显式映射全部已知 bucket：`pass`/`skipping` 为通过，`fail`/`cancel` 为失败，`pending` 为运行中；未知值按非通过处理并回归测试。
- `pathWithin` 在 Windows 跨卷 `filepath.Rel` 失败时返回“不在仓库内”，而不是错误地过滤该 PATH 项；同卷仓库内部路径仍必须过滤。

### 3. 发布工程

- 新增常规 CI，覆盖最低支持 Go 1.25.14 与发布 Go 1.27.1，执行 format、`go mod tidy -diff`、test、race、vet、静态检查、漏洞扫描、shell/workflow 校验以及 6 个目标组合构建。
- release workflow 增加 tag/manifest 一致性、干净模块、测试/race/vet 与构建 gate；依赖 action 固定到已核验 commit。
- v0.2.3 采用明确的二阶段完整性流程：第一阶段在合并 commit 上打 tag 并由 GitHub Actions 构建、attest、创建不可覆盖的 Release；第二阶段下载并核验 6 个资产，将实际 SHA-256 以独立受审 commit/PR 写入 `release-checksums.txt`，再验证无 Go 环境安装走 allowlisted binary。
- 修正文档中 checksum 注释、Windows 徽章和 release/test 步骤，避免“Windows 不需要 Go”的承诺早于 allowlist 生效。

## 拆解

| 优先级 | 难度 | 类型 | 位置 | 任务与验收 | 依赖 |
| --- | --- | --- | --- | --- | --- |
| P1 | medium | correctness | `main.go` | 替换不兼容且吞 Enter 的 shell 单键读取；测试 Enter/admin/cancel/EOF | 无 |
| P1 | hard | correctness | `actions.go` | added/deleted/rename 两侧缺失时仍能打开安全 diff；错误可见 | 输入/消息模型 |
| P1 | hard | concurrency | `watch.go` | generation 隔离旧响应，切换/reload 不增加永久轮询链 | 无 |
| P1 | medium | security | `gh.go`, `security.go` | notes 按 repo + review identity 隔离；跨仓库测试 | 无 |
| P1 | medium | UX/correctness | `actions.go`, `watch.go` | 结构化 success/error 及红绿渲染；保留真实 workflow 错误 | 无 |
| P1 | easy | correctness | `gh.go` | cancel/unknown 不再显示 pass，skipping 显式处理 | 无 |
| P1 | easy | Windows | `security.go` | 修复跨卷 PATH 误过滤，并在 Windows CI 覆盖 | 无 |
| P1 | medium | release | `.github/workflows/*` | PR/push CI；release 前强制 test/race/vet/version/build gate；Go 1.27.1 | 上述代码修复 |
| P1 | medium | supply chain | `release-checksums.txt`, release 流程 | Release 后下载核验并提交 6 个二进制 digest | 上游 merge/tag/Release |
| P1 | medium | testing/docs | `TESTING.md`, `README.md` | 完整自动化、手工、安装、安全与发布后测试说明 | 测试入口稳定 |
| P1 | external | delivery | GitHub fork/upstream | 推送 fork 并创建上游 PR；合并后打 `v0.2.3` | 全部 gate 通过、上游权限 |
| P2 | easy | quality | `gh.go` | 删除或使用 staticcheck 报告的未使用 `githubRemoteAllowed` | 可并行 |
| P2 | easy | shell quality | `scripts/fetch-or-build.sh` | 清理 ShellCheck 的空 `CDPATH`、trap/条件链提示 | 无 |
| P2 | medium | tests | side-effect command layer | 为 approve/review/send/update/watch/installer stub 增加集成测试，提高 41.3% 覆盖率 | 消息模型稳定 |
| P2 | medium | reliability | subprocess layer | 为 gh/git/herdr 请求增加合理超时/取消，并正确 reap `c.Start()` 的进程 | roadmap |
| P2 | medium | security | notes creation/open | 区分 EEXIST 与不安全已有目标，进一步缩小 symlink TOCTOU | notes 隔离 |
| P2 | medium | repository handling | cwd/path validation | 从仓库子目录启动时统一定位 Git top-level | roadmap |
| P2 | easy | maintenance | dependencies/repo settings | 定期依赖更新、Dependabot/分支保护；fork 可选开启 issues/topics | roadmap/仓库权限 |

## 校验

### 自动化 gate

```bash
test -z "$(gofmt -l *.go)"
go mod verify
go mod tidy -diff
GOTOOLCHAIN=go1.25.14 go test -count=1 ./...
GOTOOLCHAIN=go1.27.1 go test -race -shuffle=on -count=10 ./...
GOTOOLCHAIN=go1.27.1 go vet ./...
GOTOOLCHAIN=go1.27.1 go run honnef.co/go/tools/cmd/staticcheck@latest ./...
GOTOOLCHAIN=go1.27.1 go run golang.org/x/vuln/cmd/govulncheck@latest ./...
actionlint
shellcheck scripts/fetch-or-build.sh
```

- 用 `CGO_ENABLED=0` 对 `linux/darwin/windows × amd64/arm64` 全部构建。
- 在 Windows runner 解析并运行 PowerShell installer 的安全回退场景。
- 回归测试覆盖 merge 输入、缺边 diff、旧 generation、消息 severity、cancel/unknown check、notes 隔离及 Windows path 语义。

### 集成与手工验收

- 用 Herdr 0.8.2+ link 插件，验证无 repo、无 PR、pending/pass/fail/cancel/merged/closed 状态。
- 在测试 PR 中覆盖 added/modified/deleted/renamed 文件，验证单文件 `ga`、全量 diff、notes 管理和同 workspace agent 发送。
- 对 approve/comment/review/workflow dispatch/update/merge/admin merge 分别验证取消、成功、API 失败和确认后状态变化。
- 验证 sidebar token 更新、workflow run watch、恶意终端控制字符/路径/refs/环境变量回归。
- Release 后确认 6 个 binary + 6 个 `.sha256`、build provenance attestation、下载 digest、全新环境安装以及三平台 smoke test。

## 风险与假设

- 当前账号不能写 `itisbryan/herdr-gh-checks`；上游 PR 可以创建，但 merge/tag/Release 是外部依赖。未获得权限前不能宣称上游发布完成。
- `release-checksums.txt` 当前为空是有意 fail-closed，但这意味着 v0.2.3 allowlist 合并前用户仍需本地 Go；必须完成二阶段 checksum 才算完整发布。
- 原生 Windows 真机当前不可用；Windows runner、交叉编译和专门单元测试能覆盖确定性逻辑，但不能替代真实终端/ACL/reparse-point 验收。
- GitHub 写操作必须在专用测试 PR/测试 workflow 中执行，避免影响真实分支或审批记录。
- Go 1.27.1 是当前 patched 发布工具链；固定精确 patch 版本以保持构建结果和 checksum 可重复。
