# OpenSync for fnOS

独立重建的飞牛 Native 应用，前端使用 React、TypeScript 和 Semi Design。任务总览、实时执行、历史明细采用 Cloud Sync 的管理工作流；同步仍由 OpenList / AList 完成，不新增同步模式或改写同步算法。

## 功能边界

- 保留多源、多目标、三种原有同步方式、Cron / 间隔 / 手动调度、缓存、大小过滤和排除规则。
- 保留实时 SSE / 轮询、扫描和传输状态、执行历史、停止、重试、明细过滤及五种通知渠道。
- 不再注册原应用的登录、初始化账号和密码恢复路由。飞牛统一网关负责 NAS 登录；后端检查网关 UID 和管理员身份。没有第二次应用登录，但不是向匿名网络开放。
- 本地存储接入飞牛共享目录授权及用户 ACL 检查，支持映射到已有引擎虚拟目录。此版本没有本地文件同步，也不会直接读取、复制或删除映射目录中的文件。
- OpenSync 的授权不会授予 OpenList 权限。必须自行在 OpenList / AList 中挂载相同目录；保存映射仅验证引擎路径可访问，无法证明其与本地目录内容一致。

前端支持桌面侧栏、移动端底部导航、浅色 / 深色模式、宿主主题初始化与 Web 宿主主题事件；移动宿主不调用不支持的事件监听。当前文案为中文，未声称完成多语言翻译。

## 本地开发

需要 Node.js 22.12+、npm、Go 1.26.6（允许 Go 自动下载对应工具链）、Chrome（浏览器测试）。

```sh
cd frontend
npm ci
npm run dev
```

另开终端：

```sh
cd backend
go run ./cmd/server --dev --port 8030
```

打开 http://127.0.0.1:3010/app/opensync/ 。开发服务仅绑定 127.0.0.1，并使用显式开发身份；环境中存在 `TRIM_APPNAME` 时拒绝开发模式。本地存储系统授权仅能在飞牛环境验收，预览不会伪造授权结果。

开发数据位于 `backend/data/`，可用 `OPENSYNC_DATA_DIR` 指定独立目录。不要将开发服务通过隧道或反向代理公开。前端代理保持原始 Host，生产服务不提供 TCP 监听。

## 验证

```sh
cd frontend
npm test
npm run build
npm run test:browser
```

浏览器测试需要上述前后台服务，窗口尺寸覆盖 1100×640 默认桌面、1440×900 宽屏、768×900 平板、390×844 手机和 320×740 小屏。工作流测试仅替代外部引擎 HTTP 服务，实际应用接口、SQLite 和界面不替代；自动清理测试创建的任务及引擎。测试应针对独立开发数据运行，不能指向现有生产实例。

```sh
cd backend
go test ./...
go test -race ./internal/platform ./internal/config ./cmd/server
go build -o /tmp/opensync-fnos-lifecycle ./cmd/server
cd ..
node --test scripts/package.test.mjs
OPENSYNC_TEST_BINARY=/tmp/opensync-fnos-lifecycle node --test scripts/lifecycle.test.mjs
```

后端可直接运行单元测试；全新克隆没有前端构建文件时，`web/.gitkeep` 仅用于 Go embed 编译，不包含可访问页面。构建前端后才可以运行单二进制页面或打包。

本轮本地验证：11 项前端单元测试、68 项浏览器测试通过、2 项移动端不适用的桌面侧栏断言跳过；全部 Go 测试、平台相关 race 检查及原生生命周期测试通过；x86 / ARM64 安装包通过 fnpack 构建。浏览器工作流覆盖真实应用接口的引擎 / 任务保存与通知发送，但不能替代真实 OpenList 驱动的端到端同步。浅色 / 深色画面与图标加载已检查。任务页构建分块约 712 kB（gzip 187 kB），Vite 仍有大分块提示，后续可继续拆分执行明细与编辑器。

## 飞牛打包

按官方文档下载对应开发机的 [fnpack 1.2.3](https://developer.fnnas.com/docs/cli/fnpack/)，放到 `.tools/fnpack`，或者以 `FNPACK` 指定路径。

```sh
node scripts/build.mjs amd64
node scripts/build.mjs arm64
```

生成 `dist/opensync-amd64.fpk` 和 `dist/opensync-arm64.fpk`。分别声明 `platform=x86` 与 `platform=arm`，不是包含架构二进制却声明 `all`。包中只有 Go 二进制与 UI 入口资源，不依赖 NAS 上的 Node、Docker 或额外数据库服务。

`fnos/` 来自官方 `fnpack create` Native 模板。系统版本声明依据所使用的 API 最低版本为 1.2.0401；移动端开放能力要求飞牛 App 1.34.0+。这不是已完成设备兼容测试的承诺。

## 运行约定

| 内容 | 系统目录 |
| --- | --- |
| 二进制、内嵌前端、网关 Socket | `TRIM_APPDEST` |
| `config.ini` | `TRIM_PKGETC` |
| SQLite、加密密钥、路径映射、日志、升级备份 | `TRIM_PKGVAR` |
| 启动锁、临时生命周期信息 | `TRIM_PKGTMP` |

入口为 iframe，网关前缀 `/app/opensync`，Socket 为 `${TRIM_APPDEST}/app.sock`。窗口标题由宿主管理，应用不重复绘制飞牛标题栏。入口禁止普通用户自助获得访问权限，后端仍强制检查管理员身份。

应用使用专用包用户。仅声明 `trim.file.sharedAccess` 和 `trim.file.userAcl`，没有 root、全盘权限、额外共享数据目录或固定服务端口。`TRIM_API_TOKEN` 每次从环境读取，仅通过系统 Unix Socket 调用开放 API，不持久化、不返回浏览器。

`cmd/main` 提供幂等启动 / 停止及 0 / 3 状态码。停止等待任务安全退出，不强制 KILL；失败写入 `TRIM_TEMP_LOGFILE`。PID 与实际可执行文件比对，防止误杀其他进程。Linux 下 `/proc` 和网关对 0660 Socket 的访问权限仍需在设备上验证。

升级前停止服务，将数据库及 WAL、密钥、映射和配置备份到 `TRIM_PKGVAR/backups/upgrade.*`；备份含敏感凭据，不作为用户共享目录。不会自动删除历史备份。卸载脚本不主动删除用户文件或数据目录；飞牛应用中心选择是否保留应用数据时，需按其提示操作。

## 旧数据迁移

不会自动读取原 OpenSync 项目或旧实例。需要迁移时：先停止旧实例及新应用，离线备份完整旧数据目录，再将 `openSync.db`（存在时包括 `-wal` / `-shm`）与对应 `secret.key` 一起迁入新应用的 `TRIM_PKGVAR`，`config.ini` 放入 `TRIM_PKGETC`。必须授予专用包用户对应目录权限，不能以公开可写权限解决。没有匹配密钥时无法恢复加密的引擎及通知凭据。

旧数据库中的账号记录不再用于登录。上线前检查每项同步任务及引擎映射，不要让旧、新实例同时执行同一批任务。

## 设备验收

发布前必须在实际飞牛设备验证以下内容，本地测试和 fnpack 格式检查不能替代：

- 两种架构的安装 / 升级 / 启动 / 停止 / 重启和数据保留。
- 统一网关会话、管理员 / 普通用户隔离、跨域写入防护、Socket 权限、SSE 长连接。
- 飞牛桌面 iframe 和移动 App 中的 SDK 初始化、宿主主题、系统目录授权及授权回调。
- 目录 ACL 拒绝、撤销授权、软链接越界及不可达引擎的错误处理。
- 使用真实 OpenList / AList 驱动执行三种既有同步模式、多目录任务、停止 / 重试和五种通知。
- 与指定飞牛系统设置参考图进行视觉验收，以及窄窗口长路径与实时文件明细。

## 来源

MIT 许可见 LICENSE。复用原 OpenSync 的 Go 同步服务、数据模型、SQL 映射、任务调度、通知发送、相关测试、前端任务格式化与实时任务状态工具。未复制旧页面组件、旧服务入口、账号数据或密钥。新建前端页面、平台适配及打包流程，原项目目录和 Git 历史不修改。

官方参考：[Native 案例](https://developer.fnnas.com/docs/examples/native/)、[统一网关](https://developer.fnnas.com/docs/core-concepts/gateway-registration/)、[开放 API 调用](https://developer.fnnas.com/api/calling/)、[共享授权](https://developer.fnnas.com/api/authorization/shared-access/)、[文件 ACL](https://developer.fnnas.com/api/authorization/file-acl/)。
