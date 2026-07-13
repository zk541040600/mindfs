# 实施计划：MindFS 安全与连接审计修复

1. 在 `web/src/services/session.ts` 收紧显式断开的状态，使 watchdog 与异步开连遵守无活动根目录的不变量。
2. 在 `server/internal/e2ee` 增加最小的一次性消费记录与过期清理，并在 HTTP、WebSocket 证明和 E2EE 握手的成功验证路径接入。
3. 在 `server/internal/e2ee/config.go` 确保无内容变化的既有配置也会收紧为 `0600`。
4. 通过握手版本协商引入 v2：将版本绑定到派生密钥和 accept proof；WebSocket 使用方向独立的单调 sequence；v2 HTTP/原始文件响应以 proof（及 content type）作为 AES-GCM AAD。
5. 仅允许显式用户输入修改配对码；未认证 HTTP/WebSocket E2EE 错误最多使当前会话失效。
6. 在 `fs.RootInfo` 复用解析后根边界检查：保持根内符号链接可用，拒绝根外目标；收紧 `.mindfs` 的路径隔离，并让上传、读取、原始下载和 watcher 复用该边界。
7. 以已解析的配置 shell 作为长期终端缓存键，使隐式默认与显式默认选择复用会话状态；不改动 allowlist 与关闭会话时的全变体清理。
8. 将 hosted Agent config 限为本地已知 Agent 的非执行简介补充，防止远端 command/args/env/protocol、shell、relay 或新增 Agent 进入 pool/prober 的进程边界。
9. 将 Windows detached update 脚本的 bridge 目标根目录绑定到 `dstAgents`，并将脚本构造保持为可跨平台单测的纯函数。
10. 为连接生命周期、证明重放、握手 nonce 重放、v2 帧与响应绑定、权限收紧、软链越界、metadata 路径隔离、shell 键归一化、hosted 配置边界和 Windows 更新路径增加或扩展紧邻测试；不新增测试框架。
11. 使用 Go 1.25 运行全量测试与 vet、E2EE/API/usecase/fs/commandexec/agent race，运行前端 typecheck、build 和 Playwright；用 `git diff --check` 复核变更边界。

## 风险与检查点

- 证明消费只能发生在所有认证检查之后，否则无效流量可耗尽合法请求。
- 重放记录必须在锁内判断与写入，并清理过期记录，避免并发竞态和无界增长。
- 通过 v1/v2 协商保留已发布客户端的报文兼容性；当前浏览器只协商 v2，避免静默降级。
- 不重启或替换运行中的服务；验证限于源代码构建与测试。
