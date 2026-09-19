# Worktree 隔离规范

当前项目已启用 **Git Worktree 隔离模式**（由 TophantAI autoWorktree 设置控制）。

> ⚠️ **当前主分支为 `main`**，`EnterWorktree` 必须在此分支上调用，新 worktree 才会基于正确的代码基线创建。

## 规则

在对项目文件进行任何 **新建、编辑、删除** 操作之前，**必须先** 调用 `EnterWorktree` 工具切换到隔离的 git worktree 分支，然后再进行操作。

## 标准流程

1. 收到代码修改任务 → 用 `git branch --show-current` 确认当前分支是 `main`
   - 如果不是，先执行 `git checkout main` 切回主分支
2. **用 `git status` 检查是否有未提交的改动**
   - 如果有，先提醒用户并执行 `git stash` 或让用户先 commit，再继续
   - 未提交的改动不会随 worktree 带走，合并时会引发冲突
3. 调用 `EnterWorktree` 创建隔离分支（将基于当前 HEAD 创建新分支）
4. 在 worktree 分支中完成所有文件修改并提交（`git add <files> && git commit -m "..."`）
5. 完成后**必须主动询问用户**："改动已完成，是否合并回 `main`？"
   - 用户确认 → 按下方"合并回主分支流程"执行
   - 用户拒绝 → 告知 worktree 分支名，提示用户可稍后手动合并

## 合并回主分支流程

> ⚠️ `EnterWorktree` 的"退出提示合并"仅在独立会话退出时触发，**同一对话内使用 `EnterWorktree` 不会自动弹出提示**，必须手动执行以下步骤：

1. 确认 worktree 内改动已全部 commit
2. 切回主仓库根目录执行合并：
   ```
   cd <项目根目录>
   git merge <worktree-branch> --no-ff
   ```
3. 合并成功后清理 worktree 分支：
   ```
   git worktree remove .claude/worktrees/<name> --force
   git branch -d <worktree-branch>
   ```
   若 `--force` 仍报错（目录被其他会话占用，Windows 文件锁），改用备用方案：
   ```
   rm -rf .claude/worktrees/<name>
   git worktree prune
   git branch -d <worktree-branch>
   ```

## 注意

- `EnterWorktree` 基于**当前 HEAD（已提交状态）** 创建新分支，工作目录的未提交改动不会被带入
- 若主分支工作目录有未提交改动，合并 worktree 时同区域文件会产生冲突
- 不要在已有的 worktree 目录中再次调用 `EnterWorktree`
- **worktree 可能基于较旧的 `main` 提交创建**（其他 worktree 的改动尚未合并回主分支时）。若在 worktree 中发现代码与预期不符、缺少某些功能或文件，应主动执行 `git merge main` 将主分支最新代码合并进来，再继续工作。

## 例外（以下情况不需要 worktree）

- 仅读取 / 查看文件（不做任何修改）
- 用户明确说"直接改主分支"或"不用 worktree"
- 执行 shell 命令但不涉及文件写入
