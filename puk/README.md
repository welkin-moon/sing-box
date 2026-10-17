# PUK 自适应客户端

基于 sing-box 的独立 Go 控制模块，保留上游核心与普通订阅兼容性。
不依赖 Workers、DO、R2 或中心服务器进行实时择路。

整体模块划分、BBR 基线优先级及未完成能力见[系统设计与交付顺序](ARCHITECTURE.md)。

新增的 [上下行分离实验客户端](split/README.md) 通过 XHTTP 关联 CF 上传和 HY2 下载，
提供 Windows x64 启动器及 Android arm64 Throne 完整配置；[验证记录](split/VERIFICATION.md) 列出已测项目与限制。

新增 [原生双通道模块 PUK Bond](bond/README.md)：上传、下载各选一个配置档，支持已有代理链和自有汇合服务；XHTTP 实验保留为兼容实现。

## 已实现

- 保留原订阅节点，为候选节点建立独立 HTTP CONNECT 测试入口。
- 同时检查 Google、ChatGPT TLS/HTTP、跨站下载，正常验证证书。
- 失败/恢复连续计数、切换保持时间、优先级与成本排序。
- 使用 Clash API 控制 sing-box selector，切换后读回确认，失败恢复旧值。
- Cloudflared Access TCP 子进程管理：启动检查、退避重启、只清理自己启动的进程。
- 中文浏览器 GUI、CLI、交互式命令 TUI、MCP stdio，共用运行中守护进程。
- Google Direct 候选独立分组；HY2 不装入 Cloudflared TCP。

## 构建与运行

Go 1.23+；原生 Bond 的 WebSocket 载体使用已固定版本的 coder/websocket。

```sh
cd puk
go test -race ./...
go build -o puk ./cmd/puk
puk generate --input subscription.json --out-dir runtime-v1
puk run --config runtime-v1/puk.json --core /absolute/path/sing-box --core-config runtime-v1/sing-box.json
```

默认本地代理入口 `127.0.0.1:19080`，GUI `http://127.0.0.1:19100`，
核心控制接口 `127.0.0.1:19101`。随机控制令牌只保存在本地配置中。
不会自动更改系统代理、TUN、Throne 配置或已有 sing-box 进程。

Throne 可以增加指向 `127.0.0.1:19080` 的 SOCKS 节点，或使用生成的完整配置。
生成配置继承已有规则；新主入口明确使用 `PUK-Auto`，继承的其他入站规则保持原样。
Direct 组可以在 GUI/CLI 单独选择，它不会被混入 WARP 默认组。

```sh
puk status --config runtime-v1/puk.json
puk probe --config runtime-v1/puk.json
puk select --config runtime-v1/puk.json --selector PUK-Auto --tag UK-Cloudflared
puk tui --config runtime-v1/puk.json
puk mcp --config runtime-v1/puk.json
puk stop --config runtime-v1/puk.json
```

TUI 是命令式终端界面，支持 `status`、`probe`、`select <selector> <tag>`、`help`、`quit`。
MCP 客户端配置 program 为 `puk`，args 为 `mcp --config <absolute runtime path>`。
MCP 不启动第二个核心；它连接已运行的守护进程，输出仅含 JSON-RPC。

## 双端 Cloudflared

客户端 sing-box → 本地 cloudflared access tcp → Cloudflare → VPS cloudflared → VPS Reality 入站。
服务端 Tunnel 应给独立 hostname 配置 `tcp://127.0.0.1:443`。
现有 WS Cloudflared 节点保留。

修改 `examples/attachments.json` 中的 hostname、本地 cloudflared 绝对路径、原节点 tag：

```sh
puk generate --input subscription.json --attach attachments.json --out-dir runtime-access-v1
```

附件会复制原 Reality TCP 节点，更换本地连接地址，保留 UUID、公钥、SNI、flow。
若 Access hostname 有认证策略，先满足该策略；本地端口监听不代表认证成功。
整条线路必须通过 HTTPS 探测后才允许自动入选。

## 安卓状态

`mobile` 提供 gomobile 可绑定接口 `NewController/Start/Stop/StatusJSON/Probe/Select`，
要求宿主传入 `VpnService.protect(fd)`，代理与控制接口套接字均使用该保护。
该接口是供 SFA 客户端集成的库，不是已完成的 Android APK。
正式集成还需 SFA 前台服务的生命周期、本地 Cloudflared 嵌入/打包和 UI。
不能仅靠订阅启动 Android 辅助进程。

## 边界

HTTP 403 不是 SSL 失败，也不代表登录或聊天业务可用；默认只把指定检测路径的 200/204 视为通过。
跨站 HTTPS 探测无法证明所有网站或桌面应用都正常。
切换影响新连接，不能把已有 TCP/QUIC 会话迁移到另一节点。
当前只支持 HTTP(S) 测试代理，本地 CONNECT 入口避免直连测试误报。
32 个候选的全量检测会产生实际请求；默认每 120 秒一轮，最大并发 4。
可通过本地策略配置减少候选和检测频率。

## 更新与回滚

生成器拒绝覆盖已有目录；每次更新生成新版本，先运行 `sing-box check` 并验证代理。
退出守护进程会停止其拥有的核心及辅助进程，已有客户端配置不受影响。
网络切换失败会尝试恢复原 selector；如果原选择器也不可访问，状态保持未验证。
个人订阅、令牌和诊断日志不得提交 Git。
