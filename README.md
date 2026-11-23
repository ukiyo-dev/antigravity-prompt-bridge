# antigravity-prompt-bridge (CLIProxyAPI v8 Plugin)

CLIProxyAPI v8 的 C ABI `RequestNormalizer` 插件，用于调整发往 Antigravity 的请求结构。测试覆盖本地宿主加载和协议转换，不验证上游对请求的接受情况。

## 请求转换与宿主兼容

1. **Chat Completions 兼容**：在 CPA 的 `request.normalize` 阶段处理已转换的 `system` 和 `developer` 消息。
2. **修复 `sessionId` 跨会话撞车**：在将静态系统提示词插入 `request.contents[0]` 前，预先对首条真实用户消息计算 FNV-1a 64 位哈希写入 `request.sessionId`（若请求本身携带会话头，CPA 仍优先使用显式会话身份）。
3. **保留 Claude 穿插思考能力**：自动识别 CPA 为 Claude 思考模型注入的 `Interleaved thinking is enabled...` 提示并保留在真 `systemInstruction` 内。
4. **确认语语言自适应**：默认 `ack_language: auto`，英文提示词使用英文确认语，中文提示词使用中文确认语，消除纯英文编码场景下的中文偏置。
5. **支持 `smart` 智能按需移位**：仅对超长提示词或命中第三方 Agent 特征词的请求执行移位，轻量自定义请求保留在原生 `systemInstruction` 。
6. **可选单函数工具拆组**：`split_tool_groups: true` 时把 `request.tools` 的函数声明按单函数分组。

## 编译方法

### Windows (`amd64`，需要 MinGW-w64)
如果 MinGW-w64 不在 `PATH` 最前面，请先调整 `PATH`。

```powershell
$env:CGO_ENABLED = "1"
go build -trimpath -ldflags="-s -w" -buildmode=c-shared -buildvcs=false -o dist/antigravity-prompt-bridge.dll .
```

### Linux (`amd64`)
```bash
CGO_ENABLED=1 GOAMD64=v1 CGO_CFLAGS="-march=x86-64" \
  go build -trimpath -buildvcs=false -buildmode=c-shared -o dist/antigravity-prompt-bridge.so .
```

## CLIProxyAPI `config.yaml` 配置示例

将编译产物放入 CPA 的 `plugins/` 目录后，在 `config.yaml` 中启用：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    antigravity-prompt-bridge:
      enabled: true
      priority: 10
      # always: 所有非空 Agent 系统提示词一律移位
      # smart:  仅对 >= smart_min_bytes 或命中 Claude Code/Codex/OMP 等特征的提示词移位
      mode: "always"
      # auto | en | zh
      ack_language: "auto"
      smart_min_bytes: 1200
      prefix_when_empty: false
      split_tool_groups: false
```

## 本地验证（无需上游账号或网络请求）

`go test ./...` 包含 CPA 宿主加载临时动态库并执行转换流水线的集成测试。
Windows 上运行测试和构建前都应按上文将 MinGW 的 `bin` 放在 `PATH` 最前面；
即使 `gcc --version` 正常，未调整优先级时 `cgo` 仍可能只报 `exit status 2`。

Windows 上也可以先构建到 `dist/`，不覆盖 CPA 正在使用的插件，再直接验证 C ABI：

```powershell
go build -trimpath -buildvcs=false -buildmode=c-shared -o dist/antigravity-prompt-bridge.dll .
python testdata/native_abi_smoke.py dist/antigravity-prompt-bridge.dll
```

Linux 上可将相同的 Python 脚本指向构建好的 `.so`。冒烟测试验证 ABI 版本检查、
非法请求缓冲区的错误响应，以及失败后仍可正常注册插件。

## GitHub Actions 构建

`Build` 工作流在推送到 `main` 或手动触发时构建 Windows amd64。通过 CPA 集成测试和最终 DLL 的 ABI 冒烟测试后，上传 DLL、C 头文件和 `SHA256SUMS.txt`。工作流不使用上游账号，不创建 Release。Linux 构建命令仅供本地使用，未纳入 CI 验证。
