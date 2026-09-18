<!-- TOPHANTOPS:COMMIT_FLOW:START -->
<!-- 更新提交代码流程时只替换本标记块内内容，不要新增第二段。 -->
## TophantAI 提交代码流程

1. 提交文案优先使用中文，清楚说明本次变更内容，避免重复描述；代码本身（变量名、函数名、类名等标识符）保持英文，遵循项目原有命名规范。
2. 所有 AI 辅助提交必须携带代码率统计标签；使用 TophantAI 内置提交/合并能力时客户端会自动补齐：
   ```
   AI-Coding: true
   AI-Tool: TophantAI
   ```
3. 手动执行 `git commit` 时，必须把上述标签放在提交正文末尾；若文案中已有同名标签，不要重复追加。
4. 提交成功后，客户端会自动读取 `git remote get-url origin` 和当前分支，并在能匹配到项目 ID、登录 token 且存在远程仓库地址时调用管理端 `/repo/metadata:batch` 更新仓库关联。
5. 没有 `origin` 远程地址的本地仓库不更新项目 Git 信息，不使用目录名兜底。
<!-- TOPHANTOPS:COMMIT_FLOW:END -->
