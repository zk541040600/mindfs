# MindFS 安全与连接审计修复

## Goal

修复审计已证实的连接生命周期和 E2EE 安全缺陷，使显式断开真正停止连接活动、受保护请求、响应、握手和 WebSocket 帧不可被中继重放，并确保已有 E2EE 密钥配置的文件权限持续收紧。

## Confirmed Facts

- `web/src/services/session.ts` 的 `disconnect()` 关闭 socket 后仍保留 `rootId`；单例 watchdog 每 3 秒调用 `ensureReconnectLoop()`，会重新建连。
- E2EE HTTP 与 WebSocket 证明仅绑定方法、路径、时间戳和客户端 ID；服务端未消费已验证的证明，因此有效时间窗内的密文请求可被中继重放。
- `/api/e2ee/open` 验证握手 HMAC 后不记录已用 `client_nonce`；捕获的请求可再次创建并替换同一客户端的服务端会话。
- `EnsureConfigAtPath` 仅在配置内容变化时调用原子写入；完整但权限过宽的既有 `e2ee.json` 不会被改回 `0600`。
- 旧 E2EE WebSocket 只有 AES-GCM 保密性与完整性，没有每帧顺序约束；同一合法帧可在同一会话内重复投递。
- 旧受保护 HTTP 与 raw-file 响应没有绑定到发起该请求的 proof；已截获的响应密文可被替换为后续请求的响应。
- 浏览器会根据未认证的 HTTP E2EE 错误清除本地配对码，攻击者可以借此制造持久本地拒绝服务。
- `fs.RootInfo` 对相对路径只做词法根目录检查；根内指向根外的符号链接会被 `os.Stat/Open/MkdirAll` 跟随，影响文件读取、原始下载、上传和 `.mindfs` 元数据目录。
- `resolveMetaPath` 会将 `.mindfs/../...` 折叠到受管根目录其他位置，不能保证 metadata namespace 隔离。
- `commandexec.StartInSession` 用原始请求的 shell 字符串分配长期终端；省略默认 shell 后又显式选择同一 shell 会创建第二个进程，丢失 `cd` 等会话状态。
- `server/app` 会周期性拉取远端完整 Agent 配置并将其合并进本地进程配置；远端定义的 command、args、env、protocol 与新增 Agent 能进入自动探测及后续子进程启动。
- Windows 自更新的 detached PowerShell 脚本会使用未赋值的 `$shareDir` 计算 Pi SDK bridge 目标路径；严格错误模式下更新重启失败。
- 当前代码库的后端全量测试和 vet、前端 typecheck 与生产构建均已通过；后端验证须显式使用 `/root/.local/go1.25/bin/go`，系统 PATH 中的 Go 1.17 不兼容 `go.mod`。

## Requirements

- 显式断开后不保留可触发重连的连接目标，也不允许进行中的异步建连在没有活动根目录时恢复 socket。
- 对成功校验的 E2EE HTTP 与 WebSocket 证明实施有界、过期清理的一次性消费；重复证明返回既有的认证失败语义，且不延长会话存活时间。
- 对成功校验的 E2EE 握手 `client_nonce` 实施一次性消费，避免重放替换活动会话；正常的新握手不得受影响。
- 以版本协商引入 E2EE v2：将版本写入密钥派生和 server-accept proof，且对 v2 WebSocket 双向帧使用单调 sequence；服务端保留 v1 已发布客户端兼容路径。
- v2 受保护 JSON 响应以请求 proof 为 AES-GCM AAD；raw-file 响应额外绑定 content type。浏览器只允许按该请求 proof 关联过的 `Response` 解密。
- 未认证的 E2EE 错误最多清除当前会话，绝不自动删除已保存的配对码。
- 受管根目录的所有实际文件操作必须在词法检查后逐段解析已有符号链接，并要求解析目标仍在根内；安全的根内链接继续支持。`.mindfs` 路径不得使用绝对路径或 `..` 逃出元数据目录。
- 无论 `e2ee.json` 内容是否需要更新，已有配置文件都必须收紧为 `0600`，且保留原子写入和错误传播行为。
- 长期 shell 的缓存键必须按已解析的受配置允许 shell 归一化；同一默认 shell 的隐式与显式选择必须复用状态，且不能绕过 shell allowlist。
- 远端 Agent 配置仅可补充本地已知 Agent 的非执行展示字段；command、args、env、protocol、cwd、shell、生命周期命令、备份来源与新增 Agent 必须始终由本地配置决定。
- Windows detached 更新脚本必须从已传入的 agents 目标路径派生 bridge 目标根目录，且该路径构造可在非 Windows 测试环境中回归验证。
- 延续项目既有实现边界和测试风格；不引入新框架、全局中间件或兼容层，不降低已有 API 的兼容性。

## Acceptance Criteria

- [x] 卸载或显式断开后，连接 watchdog 和延迟中的建连不会重新打开 WebSocket；后续显式 `connect()` 仍能正常建立连接。
- [x] 同一有效 E2EE 请求证明第二次使用会被拒绝，第一次有效请求仍成功，且过期或失败的证明不会占用消费记录。
- [x] 重放 `/api/e2ee/open` 的同一 `client_nonce` 不会替换现有会话；新的随机 nonce 可以建立新会话。
- [x] 已完整配置且权限为 `0644` 的 E2EE 配置文件在 Ensure 后为 `0600`。
- [x] 同一 v2 WebSocket 帧重复投递会在服务端或浏览器被拒绝；双向 sequence 仅作用于所属会话，v1 兼容路径仍可解码。
- [x] v2 JSON 与 raw-file 响应仅能使用生成它们的请求 proof（及 raw content type）解密，旧密文不能作为后续请求的响应被接受。
- [x] 伪造的明文 `e2ee_proof_invalid` 不会删除本地配对码。
- [x] 根内、根外符号链接分别保持可用与被拒绝；读、原始下载/打开、上传和 metadata 写入均不会离开受管根目录。
- [x] `.mindfs` 元数据路径拒绝 `..`、绝对路径和指向根外的 `.mindfs` 链接。
- [x] 同一会话先隐式使用默认 shell、后显式选择该 shell 时复用长期终端并保留工作目录状态。
- [x] 周期性远端 Agent 配置刷新不能修改或新增任何可执行 Agent 定义，也不能改变本地 shell/relay；本地缺失的简介可安全补充。
- [x] Windows 更新重启脚本定义 bridge 目标目录并正确派生安装/便携布局；更新包测试和 Windows 交叉编译通过。
- [x] `/root/.local/go1.25/bin/go test ./... -count=1` 与 `/root/.local/go1.25/bin/go vet ./...` 通过；`yarn typecheck`、`yarn build` 和现有 Playwright 覆盖通过；`git diff --check` 无输出。

## Out of Scope

- 未由证据支持的全仓库重构、替换加密算法或性能优化。
- 构建、安装、替换或重启正在运行的 MindFS 服务。
