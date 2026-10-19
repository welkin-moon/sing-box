# 验证记录：2026-10-17

## 通过

- 独立 PUK 模块：Go 1.25.5 `go test -race ./...`、`go vet ./...`。
- Linux amd64 配置工具及 Windows amd64 启动器编译。
- Go 生成的真实配置经 Windows Xray 26.3.27 `run -test` 验证通过。
- Windows x64 → CF HTTPS 上传 → TW XHTTP 后端，下载连接经 HY2 到达相同后端。
  Cloudflare trace 返回 HTTP 200，出口 TW、`warp=off`。
- 8 MiB 确定性数据双向传输，上传端、服务器响应、下载文件 SHA-256 一致：
  `7d212b9c884f5c77896de960ae17cc341cda43b14d6a971f34ca29ebd4badf7f`。
- 8 路并发 HTTPS trace 请求全部返回 200。
- 将 HY2 密码替换为无效测试值：请求失败，HTTP 000。
- 将上传目标替换为本机未监听端口：请求超时，HTTP 000。
  两种失败均未通过其他出口继续访问。
- 临时数据测试服务只监听 VPS loopback，测试后已停止；实验 XHTTP 服务仍仅监听 loopback。
- Android 官方 APK 内容为 arm64-v8a；manifest minSdk 24、targetSdk 35，覆盖 Android 10。
  打包时以固定 SHA-256 校验，APK 不修改、不重新签名。

## 速度与边界

单次 CXL Windows 测试：下载约 34.5 Mbps，上传约 7.7 Mbps。
这是测试机与 TW 之间的结果，不是用户手机线路测速，也不构成对速度提升的保证。
并发验证为 8 路小型 HTTPS 请求，不是多线程吞吐测试。

尚未验证：Android 真机安装、VPN 保护与连接；Throne GUI 完整导入与连接；
Windows 10 实机；长时间运行、移动网络切换、Netflix/ChatGPT 等特定应用。
Windows GUI 可在独立数据目录启动，但这不等于导入与连接通过。

当前包采用官方 Throne 加完整 Xray 配置和新编译的 PUK 生成器/启动器，
尚未提供新编译的 Android 原生界面或 sing-box 原生 XHTTP 实现。
UDP 双向走 HY2；TCP 分离也仍包含各自外层协议的双向握手与确认。
