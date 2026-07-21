---
name: rebase-to-weishaw
description: 将当前本地分支 rebase 到 Wei-Shaw/main，强制执行提交 range-diff 与功能语义兼容性审查，验证后把最终 HEAD 推送到 origin/main。仅在用户明确调用该技能或明确要求执行此 rebase/审查/push 流程时使用。
---

# Rebase To WeiShaw

## 目标

把当前本地分支更新到 `Wei-Shaw/main` 之上，在 push 前确认本地功能已适配新上游，并将最终 `HEAD` 推送到 `origin/main`。保持整个流程可回退、可解释、可确认；不得用“没有冲突”或“测试通过”代替兼容性审查。

## 执行流程

1. 预检仓库状态。
   - 运行 `git status --short --branch`，确认当前分支、上游关系和工作区状态。
   - 如果存在未提交变更，停止执行，列出受影响文件并让用户选择处理方式；不要自动 stash。
   - 运行 `git remote get-url Wei-Shaw` 和 `git remote get-url origin`；remote 缺失时停止并询问用户。

2. 同步引用并记录 rebase 基线。
   - 分别运行 `git fetch Wei-Shaw main` 和 `git fetch origin main`。
   - 记录起始分支、起始 `HEAD`、目标 `Wei-Shaw/main` 和 `origin/main` 的完整 SHA 与短 SHA。
   - 用 `git merge-base <start-head> <target-upstream>` 确定本地提交序列的旧基底，并记录完整 SHA。
   - 运行 `git log --reverse --oneline <old-base>..<start-head>`，保存旧本地提交序列。
   - 如果 merge-base 无法准确表示本地序列，例如历史含复杂 merge 或预期固定点不同，停止并让用户确认旧基底。
   - 当前分支不是 `origin/main` 的跟踪分支时，明确说明最终将执行 `HEAD:main`。

3. 执行 rebase。
   - 运行 `git rebase Wei-Shaw/main`。
   - 成功后记录新 `HEAD`，并运行 `git status --short --branch` 确认 rebase 已结束且工作区无已跟踪改动。
   - 发生冲突时进入“冲突处理”，不要自行解决。

4. 强制执行 rebase 后兼容性审查。

   ### 4.1 提交语义审计

   - 运行 `git range-diff <old-base>..<start-head> <target-upstream>..<new-head>`。
   - 逐项确认旧提交均有映射，检查提交丢失、意外新增、补丁扩大、顺序变化和语义改变。
   - 对 range-diff 中的 `!`、`<`、`>` 逐项解释；不得只依据命令退出码判定通过。

   ### 4.2 上游变化交叉检查

   - 运行 `git log --oneline --no-merges <old-base>..<target-upstream>`、`git diff --stat <old-base>..<target-upstream>` 和 `git diff --name-status <old-base>..<target-upstream>`。
   - 列出本地功能涉及的文件，并先检查与上游改动的直接交集，再检查无文件交集的语义依赖。
   - 至少检查持久化 schema 与迁移、repository、service、handler、公共 API/DTO、鉴权与缓存失效、计费与事务、用户创建和默认初始化、依赖注入、异步任务、批处理、后台作业、旁路入口、前端类型/store/组件/i18n。
   - 跟踪上游新增或重构的入口，确认它们是否绕过本地功能原有的校验、扣费、缓存、事务或展示流程。
   - 不得把“没有文本冲突”或“现有测试通过”直接视为功能已适配。

   ### 4.3 分类结论

   - 分别记录：语义保持不变的内容、已有测试覆盖的内容、无文本冲突但存在的潜在语义冲突、确认缺失的兼容性适配。
   - 为每个潜在或确认问题记录证据、影响范围、严重程度和建议验证。

   ### 4.4 处理兼容性缺口

   - 对保持原功能语义所直接、明确必需的缺口，实施最小补充并添加聚焦测试。
   - 对业务规则不明确、数据迁移有歧义、需要破坏性处理或范围显著扩大的问题，停止该部分并向用户提出方案。
   - 补充后重新运行相关验证；若补充进入新提交，再次运行 range-diff，并把有意新增的兼容性提交与意外变化区分开。
   - push 前确认工作区干净，且所有兼容性补充已经进入最终 `HEAD`。当前任务未明确授权创建提交时，停止并询问用户；不得推送不包含补充的旧 `HEAD`。

5. 运行风险相称的验证。
   - 根据变更运行聚焦测试、构建、lint、类型检查和必要的集成测试。
   - 兼容性补充后必须重新运行受影响验证；失败或无法运行时说明具体原因，不得进入 push。
   - 上游等价 lint 必须使用 rebase 后 `.github/workflows/backend-ci.yml` 中 `golangci-lint-action` 声明的版本、参数和工作目录，不得直接使用 PATH 中未核对版本的二进制。
   - 在 PowerShell 环境运行 `scripts/run-rebase-lint.ps1 -Mode Upstream`。脚本会把精确版本安装到已忽略的 `backend/.tmp/codex-tools`，校验实际版本，并用独立的进程级超时执行上游命令。
   - 当 `Wei-Shaw/main..HEAD` 含 Go 代码变更时，再运行 `scripts/run-rebase-lint.ps1 -Mode LocalTaint -UpstreamRef Wei-Shaw/main`。该检查只分析本地变更涉及的 package，只报告本地新增行，并且只启用 fork 负责的高危污点规则。
   - 新版本工具发现问题时，先判定它属于上游基线、本地新增，还是由本地功能与上游代码组合触发；不得要求 fork 修复未被本地提交改动且可在 `Wei-Shaw/main` 独立复现的问题。
   - `--new-from-rev` 只过滤报告，不减少分析成本；本地增量检查必须同时限制 package 范围。
   - 不得通过修改上游拥有的 `backend/.golangci.yml`、`.github/workflows/backend-ci.yml`、`DEV_GUIDE.md` 或延长 `backend/Makefile` lint timeout 来绕过验证。

6. push 前汇报兼容性审查。
   - 汇报 range-diff 结果、检查过的上游变化、非文本语义冲突、实施的功能补充、新增或调整的测试和尚未解决的风险。
   - 分别汇报上游基线告警、本地新增告警和本地/上游组合语义告警。上游基线不计为本地兼容性缺口，但必须记录证据和是否已影响本地功能。
   - 汇报上游 CI 声明的 lint 版本、脚本解析出的精确版本、实际二进制版本、命令参数和结果。
   - 再次运行 `git status --short --branch`，并确认源为最终 `HEAD`、目标为 `origin/main`。

7. 推送到 `origin/main`。
   - 普通推送使用 `git push origin HEAD:main`。
   - 用户本轮已明确调用本技能或明确要求推送时，可以执行普通 push。
   - 普通 push 被拒绝为非快进时，停止并说明差异；只有用户明确批准后才使用 `--force-with-lease`。
   - force-with-lease 前重新 fetch `origin/main`，确认远端未变化，并优先固定预期 SHA，避免覆盖并发更新。

## 冲突处理

如果 `git rebase Wei-Shaw/main` 发生冲突：

例外：当唯一冲突文件是 `backend/cmd/server/wire_gen.go` 时，无需等待用户确认，可以直接处理该冲突并继续 rebase；仍需记录解决策略和验证结果。

1. 不要继续 rebase，也不要直接修改冲突文件。
2. 收集 `git status --short --branch`、`git diff --name-only --diff-filter=U` 和关键文件三方差异。
3. 解释 rebase 语义：`ours` 是已检出的 `Wei-Shaw/main`，`theirs` 是正在回放的本地提交。
4. 列出每个冲突主题、推荐策略、原因、可选路径和继续后需要运行的验证。
5. 等待用户确认；只有明确同意后才能编辑、`git add` 和 `git rebase --continue`。
6. 出现下一轮冲突时重复本流程。

## 验证职责边界

- golangci-lint/gosec 自身的性能、取消机制和规则实现由对应工具上游负责；本技能只保证使用仓库声明的版本可靠验证。
- `Wei-Shaw/main` 已存在且未被本地提交修改的业务代码、lint 配置、CI、文档和架构问题由 Wei-Shaw 上游负责；fork 不长期携带通用修复补丁。
- fork 负责本地提交、本地新增告警，以及本地功能与上游代码组合后才出现的兼容性问题。
- 对疑似上游基线问题，至少用 `git log <upstream>..HEAD -- <path>`、`git diff <upstream> HEAD -- <path>` 和必要的上游独立复现确认归属。
- fork 自有的 rebase 自动化、版本核对、进程清理和增量检查放在本技能目录，不修改上游产品文件。
- 本地污点检查配置只启用 `G701`、`G702`、`G703`、`G704`、`G707`。`G705` 和 `G706` 低于当前 high-only 策略，其中 `G706` 还有已确认的性能问题。

## 汇报要求

最终回复必须包含：

- 起始分支、起始 `HEAD`、旧基底、fetch 后的 `Wei-Shaw/main` 和 `origin/main` SHA。
- rebase 结果、冲突和解决策略。
- range-diff 结果及所有提交映射异常。
- 检查过的“旧基底 → 新上游”变化。
- 发现的非文本语义冲突与严重程度。
- 上游基线、本地新增和组合语义三类告警及其归属证据。
- 实施的功能补充及新增或调整的测试。
- 验证命令和结果。
- 尚未解决的风险。
- 推送命令和远端结果；未推送时说明阻塞原因。
