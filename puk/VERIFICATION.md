# 验证记录（2026-10-11）

开发分支：`puk/adaptive-client`。控制模块与上游 sing-box 核心分开构建。

## 实机网络

Windows MSDT，复用本机现有 sing-box 1.13.21 二进制启动独立测试实例。
原 v2rayN/HY2 实例没有修改。原订阅 32 个节点保留，额外复制一个 Reality 节点用于 Access TCP。

双端路径：本地 cloudflared Access TCP → Cloudflare → UK VPS cloudflared → 本地 Reality TCP 443。
Tunnel 原始配置在 MSDT 修改前保存。添加独立 hostname，没有覆盖现有 WS、面板、订阅入口。

| 双端线路检测 | HTTP | 证书验证 | 下载字节 |
|---|---:|---|---:|
| Google generate_204 | 204 | 通过 | 0 |
| ChatGPT /cdn-cgi/trace | 200 | 通过 | 207 |
| Microsoft 首页 | 200 | 通过 | 201253 |
| Cloudflare 1 MiB 下载 | 200 | 通过 | 1048576 |

上述为 Windows curl/Schannel 实测；不能作为桌面 ChatGPT 已登录对话或 HTTP/2 已验证的证据。
独立 Go 探测器也记录到 UK 原 Reality、HY2、WS CF 的 Google 204、ChatGPT trace 200、Microsoft 200。
原用户 HY2 代理 2081 回归 Google 204，证书验证通过。

原配置第一次启动发现控制端口与第 20 个测试端口冲突，已修复，增加 40 节点端口唯一性测试。
未覆盖旧版本运行配置；修复后生成新目录，`sing-box check` 通过。
测试过程中 MCPIO 短暂 502；测试进程后来退出，现用 sing-box 和 MCPIO cloudflared 仍在运行。

## 自动化回归

Linux：模块单元测试、race 与 vet。
Windows：实机编译、配置校验；最初发现测试用 Unix 绝对路径不能在 Windows 使用，已修正为平台绝对路径。
测试覆盖显式代理、证书验证、无直连回落、HTTP 状态允许列表、计数切换、控制器读回及回滚、并发限制、授权与来源检查、MCP、进程退避及清理。

## 尚不能宣称完成的部分

- 安卓为可绑定 Go 接口，尚无集成后的 APK。
- PC 后台控制模块与本地 GUI 可构建；没有替换现用 Throne 或启用新的系统代理。
- JP/US 直连仍可能超时，应由实际健康检查排除，原节点保留。
- 检测路径成功不代表 ChatGPT 登录、对话或所有网站可用。
