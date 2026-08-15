---
name: merge-from-weishaw
description: 将 Wei-Shaw/main 合并到当前本地分支，审查上游变化与本地功能的语义兼容性，验证后仅把最终 HEAD 推送到 origin/main，并显式禁止拉取或推送 tag。仅在用户明确调用该技能，或明确要求执行此上游 merge、兼容性审查和无 tag 推送流程时使用。
---

# Merge From Wei-Shaw

## 目标

把 `Wei-Shaw/main` 合并到当前本地分支，保留本地提交历史，完成冲突处理、双亲差异审计和功能语义兼容性审查后，仅将最终 `HEAD` 推送到 `origin/main`。不得创建、更新或推送 tag，避免上游 tag 触发 fork 的 release 工作流。

保持流程可回退、可解释、可确认。不得用“没有冲突”或“测试通过”代替兼容性审查。

## 执行流程

1. 预检仓库状态。
   - 运行 `git status --short --branch`，记录当前分支和跟踪关系。
   - 如果存在未提交或未跟踪变更，停止执行，列出受影响文件并让用户选择处理方式；不要自动 stash。
   - 运行 `git remote get-url Wei-Shaw` 和 `git remote get-url origin`；remote 缺失时停止并询问用户。
   - 当前分支不是 `main` 时，明确说明最终目标仍是 `origin/main`，并在合并前取得用户确认。

2. 只同步分支引用，不同步 tag。
   - 分别运行：
     - `git fetch --no-tags Wei-Shaw +refs/heads/main:refs/remotes/Wei-Shaw/main`
     - `git fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main`
   - 不得使用会获取或更新 tag 的 `git fetch --tags`、`git pull --tags` 或 mirror 操作。
   - 记录起始分支、起始 `HEAD`、fetch 后的 `Wei-Shaw/main` 和 `origin/main` 完整 SHA 与短 SHA。
   - 用 `git merge-base <start-head> Wei-Shaw/main` 记录旧共同基底；无法得到唯一合理基底时停止并让用户确认。
   - 运行 `git log --reverse --oneline <old-base>..<start-head>`，记录合并前本地提交序列。

3. 执行 merge。
   - 运行 `git merge --no-ff -m "merge(upstream): 合并 Wei-Shaw/main" Wei-Shaw/main`。
   - 如果上游已经是当前 `HEAD` 的祖先，接受 `Already up to date`，不要创建空提交。
   - 成功后记录新 `HEAD`，运行 `git status --short --branch`，确认 merge 已结束且工作区无已跟踪改动。
   - 发生冲突时进入“冲突处理”，不要在未确认策略前自行解决。

4. 强制执行 merge 后兼容性审查。

   ### 4.1 历史与双亲差异审计

   - 运行 `git merge-base --is-ancestor <start-head> <new-head>`，确认合并前本地历史完整保留。
   - 运行 `git merge-base --is-ancestor Wei-Shaw/main <new-head>`，确认新上游完整进入结果。
   - 对新建 merge commit 运行 `git show --no-ext-diff --cc --stat <new-head>`，检查合并解决是否引入双亲都没有的意外改动。
   - 分别运行：
     - `git diff --stat <start-head>..<new-head>` 和 `git diff --name-status <start-head>..<new-head>`，检查引入本地分支的上游变化。
     - `git diff --stat Wei-Shaw/main..<new-head>` 和 `git diff --name-status Wei-Shaw/main..<new-head>`，检查合并后相对上游保留的本地差异。
   - 逐项解释异常新增、丢失、扩大或语义改变；不得只依据命令退出码判定通过。

   ### 4.2 上游变化交叉检查

   - 运行 `git log --oneline --no-merges <old-base>..Wei-Shaw/main`、`git diff --stat <old-base>..Wei-Shaw/main` 和 `git diff --name-status <old-base>..Wei-Shaw/main`。
   - 列出本地功能涉及的文件，先检查与上游改动的直接交集，再检查无文件交集的语义依赖。
   - 至少检查持久化 schema 与迁移、repository、service、handler、公共 API/DTO、鉴权与缓存失效、计费与事务、用户创建和默认初始化、依赖注入、异步任务、批处理、后台作业、旁路入口、前端类型/store/组件/i18n。
   - 跟踪上游新增或重构的入口，确认它们是否绕过本地功能原有的校验、扣费、缓存、事务或展示流程。
   - 上游新增功能应予保留；用户可见展示必须适配本地 Codex 风格，不直接照搬与本地不一致的上游视觉样式。
   - 不得把“没有文本冲突”或“现有测试通过”直接视为功能已适配。

   ### 4.3 分类结论

   - 分别记录：语义保持不变的内容、已有测试覆盖的内容、无文本冲突但存在的潜在语义冲突、确认缺失的兼容性适配。
   - 为每个潜在或确认问题记录证据、影响范围、严重程度和建议验证。

   ### 4.4 处理兼容性缺口

   - 对保持原功能语义所直接、明确必需的缺口，实施最小补充并添加聚焦测试。
   - 对业务规则不明确、数据迁移有歧义、需要破坏性处理或范围显著扩大的问题，停止该部分并向用户提出方案。
   - 不要手动修改生成的 Ent 文件；更新 schema 或生成器后运行相应生成命令。
   - 补充后重新运行相关验证，并把有意新增的兼容性提交与意外变化区分开。
   - push 前确认工作区干净，所有兼容性补充均已进入最终 `HEAD`。当前任务未授权创建提交时，停止并询问用户。

5. 运行风险相称的验证。
   - 根据变化运行聚焦测试、构建、lint、类型检查和必要的集成测试。
   - 兼容性补充后必须重新运行受影响验证；失败或无法运行时说明具体原因，不得进入 push。
   - 上游等价 lint 必须使用 merge 后 `.github/workflows/backend-ci.yml` 中 `golangci-lint-action` 声明的版本、参数和工作目录。
   - 在 PowerShell 环境运行 `.codex/skills/merge-from-weishaw/scripts/run-merge-lint.ps1 -Mode Upstream`。
   - 当 `Wei-Shaw/main..HEAD` 含 Go 代码变更时，再运行 `.codex/skills/merge-from-weishaw/scripts/run-merge-lint.ps1 -Mode LocalTaint -UpstreamRef Wei-Shaw/main`。
   - 新版本工具发现问题时，先判定它属于上游基线、本地新增，还是由本地功能与上游代码组合触发；不得要求 fork 修复未被本地提交改动且可在 `Wei-Shaw/main` 独立复现的问题。
   - `--new-from-rev` 只过滤报告，不减少分析成本；本地增量检查必须同时限制 package 范围。
   - 不得通过修改上游拥有的 lint 配置、CI、文档或 timeout 来绕过验证。

6. push 前汇报兼容性与 tag 安全审查。
   - 汇报双亲差异审计结果、检查过的上游变化、非文本语义冲突、实施的功能补充、新增或调整的测试和尚未解决的风险。
   - 分别汇报上游基线告警、本地新增告警和本地/上游组合语义告警。
   - 汇报上游 CI 声明的 lint 版本、脚本解析出的精确版本、实际二进制版本、命令参数和结果。
   - 检查 `.github/workflows/release.yml` 的实际触发条件，说明本次只更新 `refs/heads/main`，不会推送 `refs/tags/*`。
   - 再次运行 `git fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main`，确认 `origin/main` 未发生未审查变化。
   - 运行 `git status --short --branch`，确认源为最终 `HEAD`、目标为 `origin/main` 且工作区干净。

7. 只推送 main 分支。
   - 先运行 `git push --dry-run --porcelain --no-follow-tags origin HEAD:refs/heads/main`。
   - 检查 dry-run 输出只包含 `refs/heads/main`，不得包含任何 `refs/tags/*`；发现 tag 时停止。
   - 用户本轮已明确调用本技能或明确要求推送时，运行 `git push --no-follow-tags origin HEAD:refs/heads/main`。
   - 不得使用 `--tags`、`--follow-tags`、`--mirror`，不得创建或更新 tag。
   - 普通 push 被拒绝为非快进时停止，说明差异并让用户选择重新合并最新 `origin/main`；不要自动强制推送。
   - 只有用户单独明确批准强制推送后，才可在重新 fetch、固定 lease SHA 且仍带 `--no-follow-tags` 的前提下使用 `--force-with-lease`。

## 冲突处理

当 `git merge Wei-Shaw/main` 发生冲突时：

例外：当唯一冲突文件是 `backend/cmd/server/wire_gen.go` 时，无需等待用户确认，可按生成流程解决并继续 merge；仍需记录策略和验证结果。

1. 不要提交 merge，也不要直接修改冲突文件。
2. 收集 `git status --short --branch`、`git diff --name-only --diff-filter=U` 和关键文件三方差异。
3. 解释 merge 语义：`ours` 是当前本地分支，`theirs` 是 `Wei-Shaw/main`。
4. 列出每个冲突主题、推荐策略、原因、可选路径和继续后需要运行的验证。
5. 语义等价时采用上游实现；功能互补时保留功能并完成集成；展示样式采用本地 Codex 风格。
6. 等待用户确认；只有明确同意后才能编辑、`git add` 和创建 merge commit。
7. 出现下一轮冲突时重复本流程。

## 验证职责边界

- fork 负责本地提交、本地新增告警，以及本地功能与上游代码组合后才出现的兼容性问题。
- `Wei-Shaw/main` 已存在且未被本地提交修改的业务代码、lint 配置、CI、文档和架构问题由上游负责；fork 不长期携带通用修复补丁。
- 对疑似上游基线问题，至少用 `git log Wei-Shaw/main..HEAD -- <path>`、`git diff Wei-Shaw/main HEAD -- <path>` 和必要的上游独立复现确认归属。
- fork 自有的 merge 自动化、版本核对、进程清理和增量检查放在本技能目录，不修改上游产品文件。
- 本地污点检查只启用 `G701`、`G702`、`G703`、`G704`、`G707`。

## 汇报要求

最终回复必须包含：

- 起始分支、起始 `HEAD`、旧共同基底、fetch 后的 `Wei-Shaw/main` 和 `origin/main` SHA。
- merge 结果、merge commit、冲突和解决策略。
- 本地历史与上游历史的祖先校验，以及双亲差异审计异常。
- 检查过的“旧共同基底 → 新上游”变化。
- 发现的非文本语义冲突与严重程度。
- 上游基线、本地新增和组合语义三类告警及其归属证据。
- 实施的功能补充及新增或调整的测试。
- 验证命令和结果。
- 尚未解决的风险。
- dry-run 的 ref 更新范围、无 tag 结论、正式推送命令和远端结果；未推送时说明阻塞原因。
