# OpenSync for fnOS

独立重建的飞牛 Native 应用，前端使用 React、TypeScript 和 Semi Design。任务总览、实时执行、历史明细采用 Cloud Sync 的管理工作流；同步仍由 OpenList / AList 完成，不新增同步模式或改写同步算法。

## 功能边界

- 保留多源、多目标、三种原有同步方式、Cron / 间隔 / 手动调度、缓存、大小过滤和排除规则。
- 保留实时 SSE / 轮询、扫描和传输状态、执行历史、停止、重试、明细过滤及五种通知渠道。
- 不再注册原应用的登录、初始化账号和密码恢复路由。飞牛统一网关负责 NAS 登录；后端检查网关 UID 和管理员身份。没有第二次应用登录，但不是向匿名网络开放。

前端支持桌面侧栏、移动端底部导航、浅色 / 深色模式、宿主主题初始化与 Web 宿主主题事件；移动宿主不调用不支持的事件监听。当前文案为中文，未声称完成多语言翻译。

## 本地开发

需要 Node.js 22.12+、npm、Go 1.26.6（允许 Go 自动下载对应工具链）、Chrome（浏览器测试）。

一条命令同时启动后端与前端：

```sh
node scripts/dev.mjs
```

打开 http://127.0.0.1:3020/app/opensync/ 。Ctrl-C 会停止本脚本启动的两个服务。

脚本做了三件手工步骤容易出错的事：从 PATH 与常见安装目录中挑出满足 22.12+ 的 Node 并按目录前置（`npm` 的 shebang 通过 PATH 找 `node`，只调用新版二进制而不前置目录，`npm` 仍会用旧版本）；缺少 `frontend/node_modules` 时自动 `npm ci`（`--no-install` 可关闭）；端口若已被占用则报错退出并列出 PID，不会结束不属于本脚本的进程——本仓库常有多会话并行，别的会话可能正在用这些端口。端口取自 `frontend/vite.config.ts`，日志写入 `.dev-logs/`（已忽略）。

也可以用 `npm run dev`（在仓库根目录）执行同一脚本。要手工控制进程时，仍是原来两条命令：

```sh
cd frontend && npm run dev
cd backend && go run ./cmd/server --dev --port 8040
```

开发服务仅绑定 127.0.0.1，并使用显式开发身份；环境中存在 `TRIM_APPNAME` 时拒绝开发模式。跨站写入防护在预览下按开发模式放宽：浏览器声明为 `same-origin` 的写请求直接放行，因此本地预览不需要额外配置。

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
npm run test:scripts
OPENSYNC_TEST_BINARY=/tmp/opensync-fnos-lifecycle node --test scripts/lifecycle.test.mjs
```

后端可直接运行单元测试；全新克隆没有前端构建文件时，`web/.gitkeep` 仅用于 Go embed 编译，不包含可访问页面。构建前端后才可以运行单二进制页面或打包。

本轮本地验证：11 项前端单元测试、5 项打包脚本测试通过；全部 Go 测试、平台相关 race 检查通过；amd64 安装包通过 fnpack 构建。浏览器回归共 90 项，其中 4 项为移动端不适用的桌面侧栏断言跳过；另有一组既有用例不稳定，每次运行失败项并不固定：引擎向导的树展开步骤偶发点击失效，失败后残留的引擎又会让断言空状态的用例失败，移动端项目则断言了只在桌面存在的侧栏。未改动代码的基线上同样以这种方式失败，这几处断言均不在本次改动范围内。浏览器工作流覆盖真实应用接口的引擎 / 任务保存与通知发送，但不能替代真实 OpenList 驱动的端到端同步。浅色 / 深色画面与图标加载已检查。任务页构建分块约 885 kB（gzip 234 kB），Vite 仍有大分块提示，后续可继续拆分执行明细与编辑器。

## 飞牛打包

按官方文档下载对应开发机的 [fnpack 1.2.3](https://developer.fnnas.com/docs/cli/fnpack/)，放到 `.tools/fnpack`，或者以 `FNPACK` 指定路径。

```sh
node scripts/release.mjs
```

依次执行：工作区状态检查（有未提交改动会告警，产物将无法用提交号追溯，但不中止）→ 前端 `tsc --noEmit` 与 vitest → `go test ./...` → 前端构建一次 → 交叉编译并打包 amd64 / arm64 → 写出 `dist/SHA256SUMS` 与产物清单。`--skip-tests` 可跳过测试只出包。

前端只构建一次供两个架构共用，避免重复构建产生不一致的 UI 资源。脚本不修改 git 状态：不提交、不打 tag、不推送。结尾会打印 README「设备验收」中必须在真机确认的项——打包成功不等于可以发布。

单独打包某个架构：

```sh
node scripts/build.mjs amd64
node scripts/build.mjs arm64
```

生成 `dist/opensync-amd64.fpk` 和 `dist/opensync-arm64.fpk`。分别声明 `platform=x86` 与 `platform=arm`，不是包含架构二进制却声明 `all`。包中只有 Go 二进制与 UI 入口资源，不依赖 NAS 上的 Node、Docker 或额外数据库服务。打包前会清空 `dist/opensync-<arch>/` 并跳过 `.DS_Store` 等无关文件，避免上一次构建的残留被打进包。

`fnos/` 来自官方 `fnpack create` Native 模板。系统版本声明依据所使用的 API 最低版本为 1.2.0401；移动端开放能力要求飞牛 App 1.34.0+。这不是已完成设备兼容测试的承诺。

## 运行约定

| 内容 | 系统目录 |
| --- | --- |
| 二进制、内嵌前端、网关 Socket | `TRIM_APPDEST` |
| `config.ini` | `TRIM_PKGETC` |
| SQLite、加密密钥、日志、升级备份 | `TRIM_PKGVAR` |
| 启动锁、临时生命周期信息 | `TRIM_PKGTMP` |

入口为 iframe，网关前缀 `/app/opensync`，Socket 为 `${TRIM_APPDEST}/app.sock`。窗口标题由宿主管理，应用不重复绘制飞牛标题栏。入口禁止普通用户自助获得访问权限，后端仍强制检查管理员身份。

应用使用专用包用户。不申请任何开放 API 权限（`api-scope` 为空），没有 root、全盘权限、共享数据目录或固定服务端口；用户身份只取自统一网关注入的请求头，应用自身不调用飞牛开放 API，也不读取、复制或删除任何本地文件。

跨站写入防护默认拦截浏览器自己声明为跨站或同站（`Sec-Fetch-Site: cross-site` / `same-site`）的写请求；同站指同一台 NAS 上另一个端口的应用，浏览器仍会带上飞牛会话 Cookie，因此与跨站同样处理。飞牛统一网关会改写 `Host`，应用无法据此判断自身公网地址，因此不比较 `Host`。若需要严格模式，在 `config.ini` 的 `[opensync]` 中把访问本应用的地址写入 `allowed_origins`（逗号分隔，可写完整地址或仅主机名），重启应用后所有写请求都必须与该列表精确匹配；未在列表中配置端口时接受任意端口。环境变量 `OPENSYNC_ALLOWED_ORIGINS` 只在没有 `config.ini` 时生效。被拒绝的写请求会记录 `rejected cross-site` 日志，包含 `origin`、`Host`、`Referer` 等原始头，便于在设备上定位。

`cmd/main` 提供幂等启动 / 停止及 0 / 3 状态码。启动不再要求 `TRIM_API_TOKEN`（应用不调用开放 API，平台在 `api-scope` 为空时也可能不注入），缺少它不会阻止启动。停止等待任务安全退出，不强制 KILL；失败写入 `TRIM_TEMP_LOGFILE`。PID 与实际可执行文件比对，防止误杀其他进程。Linux 下 `/proc` 和网关对 0660 Socket 的访问权限仍需在设备上验证。

升级前停止服务，将数据库及 WAL、密钥和配置备份到 `TRIM_PKGVAR/backups/upgrade.*`；备份含敏感凭据，不作为用户共享目录。不会自动删除历史备份。卸载脚本不主动删除用户文件或数据目录；飞牛应用中心选择是否保留应用数据时，需按其提示操作。

## 旧数据迁移

不会自动读取原 OpenSync 项目或旧实例。需要迁移时：先停止旧实例及新应用，离线备份完整旧数据目录，再将 `openSync.db`（存在时包括 `-wal` / `-shm`）与对应 `secret.key` 一起迁入新应用的 `TRIM_PKGVAR`，`config.ini` 放入 `TRIM_PKGETC`。必须授予专用包用户对应目录权限，不能以公开可写权限解决。没有匹配密钥时无法恢复加密的引擎及通知凭据。

旧数据库中的账号记录不再用于登录。上线前检查每项同步任务及引擎配置，不要让旧、新实例同时执行同一批任务。

## 设备验收

发布前必须在实际飞牛设备验证以下内容，本地测试和 fnpack 格式检查不能替代：

- 两种架构的安装 / 升级 / 启动 / 停止 / 重启和数据保留。
- 统一网关会话、管理员 / 普通用户隔离、跨域写入防护、Socket 权限、SSE 长连接。
- 跨站写入防护：在 UI 中新增 / 编辑 / 删除引擎、任务、通知均应成功；再从另一个页面（其他站点，或同一 NAS 上另一个端口的应用）发起写请求，应返回 403 并记录 `rejected cross-site`。判定依据是浏览器自动发出、页面无法伪造的 `Sec-Fetch-Site`，因此用 curl 复现时需带上 `-H 'Sec-Fetch-Site: cross-site'`；只带 `Origin` 而没有该头的请求会被当成非浏览器客户端，在默认模式下放行。若日志出现 `rejected cross-site`，按其中的 `origin` 把访问地址写入 `config.ini` 的 `allowed_origins` 并重启应用，即可切换到严格模式——此后所有写请求（含 curl）都必须与列表匹配。
- 飞牛桌面 iframe 和移动 App 中的 SDK 初始化、宿主主题与宿主主题事件。
- 不可达引擎、无效 Token 及超时引擎的错误处理。
- 使用真实 OpenList / AList 驱动执行三种既有同步模式、多目录任务、停止 / 重试和五种通知。
- 与指定飞牛系统设置参考图进行视觉验收，以及窄窗口长路径与实时文件明细。

## 来源

MIT 许可见 LICENSE。复用原 OpenSync 的 Go 同步服务、数据模型、SQL 映射、任务调度、通知发送、相关测试、前端任务格式化与实时任务状态工具。未复制旧页面组件、旧服务入口、账号数据或密钥。新建前端页面、平台适配及打包流程，原项目目录和 Git 历史不修改。

官方参考：[Native 案例](https://developer.fnnas.com/docs/examples/native/)、[统一网关](https://developer.fnnas.com/docs/core-concepts/gateway-registration/)、[开放 API 调用](https://developer.fnnas.com/api/calling/)。
