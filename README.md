# OpenSync

飞牛下的群晖 Cloud Sync 平替方案。

依赖 [OpenList](https://github.com/OpenListTeam/OpenList)（兼容 AList）作为存储引擎 — 在 OpenList 中添加本地存储和各类云盘（阿里云盘、百度网盘、OneDrive、S3、WebDAV 等）后，即可通过 OpenSync 在不同存储之间自动定期备份与同步文件。

开源地址：https://github.com/chenbin3625/OpenSync-fnOS

## 功能

- **存储引擎管理** — 添加、编辑、测试 AList / OpenList 引擎连接
- **同步任务** — 三种同步模式：
  - 仅新增：增量备份，不删除目标端多余文件
  - 全同步：目标与源完全一致，自动清理多余文件，相同内容优先复用避免重传
  - 移动模式：将文件从源端迁移到目标端，用于归档或腾挪空间
- **灵活调度** — 固定间隔、Cron 表达式、仅手动触发
- **文件过滤** — gitignore 格式排除规则 + 文件大小范围限制
- **实时进度** — SSE 推送扫描进度、传输速度、剩余时间、逐文件状态
- **执行历史** — 分页查看历史任务，按状态 / 类型 / 关键词筛选
- **多渠道通知** — Webhook、Server酱、钉钉、企业微信、飞书
- **系统设置** — 并发数、重试次数、超时时间、历史记录保留天数

## 使用方式

### 开发

```bash
# 安装前端依赖
cd frontend && npm install && cd ..

# 启动开发服务（前端 + 后端）
npm run dev
```

打开 http://127.0.0.1:3020/app/opensync/ ，Ctrl-C 停止服务。

### 构建

```bash
# 构建前端
cd frontend && npm run build

# 打包 fnOS 应用
npm run package:amd64
npm run package:arm64

# 完整发布（双架构 + 校验）
node scripts/release.mjs
```

### 部署到 fnOS

将打包生成的 `.fpk` 文件通过飞牛 NAS 应用管理界面安装。应用通过 Unix Socket 接入 fnOS 网关，访问路径为 `/app/opensync`。

## 技术栈

- 前端：React 19 + TypeScript + Vite + Semi Design
- 后端：Go + Gin + SQLite
- 调度：robfig/cron
- 打包：fnOS Native App

## 许可证

[MIT](LICENSE)
