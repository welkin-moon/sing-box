# PUK 上下行分离实验客户端

TCP 上传：本机 XHTTP → Cloudflare HTTPS → VPS 上的 loopback XHTTP 服务。
TCP 下载：同一 XHTTP 会话 → HY2 → 同一 loopback 服务。
网站出口仍是 VPS。每条外层传输各自仍有双向握手、确认与控制流量。
原生 UDP 单独走 HY2 双向；本方案没有把 UDP 的去回方向拆开。

采用组合内核：保留 sing-box / Throne 的现有功能，分离连接由 Xray 执行。
没有宣称标准 sing-box 原生支持 XHTTP，也没有把两个独立代理连接当成同一个会话。

## 配置

复制 `profile.example.json` 到仓库外的私人目录，填入凭据及 HY2 证书 DER 的
SHA-256 十六进制指纹。不要使用公网 CA 的指纹替代服务器证书指纹。
服务端 XHTTP 必须只监听 loopback，上传与下载最终到达同一个后端端口与 path。
后端不需要额外 TLS：上传的公网段由 Cloudflare HTTPS 保护，下载的公网段由 HY2 TLS 保护。

```sh
cd puk
go build -o puk-split ./cmd/puk-split
./puk-split generate --profile /private/profile.json --out-dir /private/runtime-v1
./puk-split run --profile /private/profile.json --xray /absolute/path/xray
```

`generate` 拒绝覆盖现有目录。`run` 先让 Xray 验证配置，使用临时配置并在退出时清理，
只停止它启动的子进程。SOCKS / HTTP 默认监听 `127.0.0.1:19080/19081`，不修改系统代理、TUN、DNS 或其他客户端。

## Windows 10+ x64 与 Android 10+ arm64

Windows 可以运行生成器/启动器，或在新版 Throne 导入 `throne-import.txt` 中的整条链接。
Android 使用 Throne for Android 2.0.2 arm64 APK，导入同一链接或 `throne-configs.json`。
这是带完整 Xray 配置的 profile：保留 `hy2-download` 和 `downloadSettings.sockopt.dialerProxy`。
不要把原始配置当作普通 sing-box 订阅导入，也不要只复制第一个 VLESS outbound。
Throne 管理本地桥、TUN 与 Android `VpnService.protect`，无需手机 Root 或额外 Termux 进程。

`package.py` 使用 SHA-256 固定的上游官方程序制作两个实验包，Android APK 原签名不变；
这不是重新签名的 APK，也不是声称已完成新的 Android 原生 GUI。
Windows 包包含独立便携 Throne、命令行启动器、Xray 和私人配置；Android 包包含 APK 和私人导入配置。
两者均不能公开上传 Release：其中的 profile 和导入链接含节点凭据。
仓库 CI 只生成不含凭据的程序与公开示例。

## 验证边界

2026-10-17 已在 TW / Windows x64 验证 CF 上传、HY2 下载的 HTTPS 请求，
出口 `warp=off`、地区 TW。数据哈希、并发请求和通道故障也已通过；Android 真机和 GUI 导入尚未验证。
详细结果以 [VERIFICATION.md](VERIFICATION.md) 为准，不能从 HTTP 200 推导速度收益或所有应用可用。
先用独立实验配置测试，原矩阵订阅保持原样。
