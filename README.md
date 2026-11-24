# Antigravity Prompt Bridge

用于 **CLIProxyAPI v8（CPA）** 的请求规范化插件。

用 Claude Code、OpenCode、Codex 等第三方 Agent 通过 CPA 调用 Antigravity 时，客户端自带的大段系统提示词如果直接放进 `request.systemInstruction`，很容易触发上游的 **HTTP 429 风控**。这个插件会在请求发往上游前调整提示词的存放位置：保留原本的系统提示词内容不变，只把它从原生系统指令挪到对话历史开头，用来避开针对请求结构的风控拦截。

## 它是怎么工作的

插件实现了 CPA 的 C ABI `RequestNormalizer` 接口，挂在 `request.normalize` 阶段工作——也就是等 CPA 先把 OpenAI、Anthropic 等入口协议统一转换成 Antigravity 格式之后，再做最后一步结构调整。

触发移位时，插件会对请求做三处改动：

| 字段 | 改动前 | 改动后 |
| --- | --- | --- |
| `request.systemInstruction` | 客户端的 Agent 系统提示词 | Antigravity 官方 CLI 的 296 字节 `<identity>` 前缀 + 桥接说明（若 CPA 注入了 Claude 穿插思考提示，仍保留在此处） |
| `request.contents` | 原始对话历史 | 合成 `user`（原系统提示词）→ 合成 `model`（简短确认语）→ 原始对话历史 |
| `request.sessionId` | 可能为空 | 已有值则保留；为空时，在挪动提示词**之前**取首条真实 `user` 文本计算 FNV-1a 64 位哈希填入 |

桥接说明会告诉模型：把紧接着的第一条 `user` 消息当作当前会话的指令来遵守，后面的用户消息才是具体任务。之所以要在移位前先算好 `sessionId`，是因为一旦把同一份静态提示词塞到 `contents[0]`，不同会话按首轮文本算出来的回退 ID 就会全部撞车。如果 CPA 已从请求头派生出会话 ID，仍以 CPA 的为准；请求里找不到可用用户文本时则不填这个回退值。

这里有一个已知取舍：**挪到 `contents` 里的提示词本质上成了普通对话历史，不再是原生 `systemInstruction`**，合成的 `model` 确认语也是插件直接填进去的占位内容，后续真实对话的顺序和内容保持原样。

插件只处理发往 Antigravity 的 Agent 请求。以下情况会直接跳过，不做提示词移位：

- 非 Antigravity 格式的请求，或明确标记为非 Agent 的请求
- 图像模型，以及只挂了原生联网搜索但没有任何函数声明的请求
- `contents` 为空、已经带有相同 `<identity>` 前缀，或属于专用搜索机器人的提示词

## 安装与配置

去 [GitHub Releases](https://github.com/ukiyo-dev/antigravity-prompt-bridge/releases) 下载和 **CPA 所在机器**系统及架构对应的压缩包（支持 Windows / Linux / macOS 的 `amd64` 与 `arm64`），对照 `checksums.txt` 校验无误后，把里面的 `.dll`、`.so` 或 `.dylib` 解压到 CPA 的插件目录。注意 Linux 下需要使用支持动态插件的 CPA 构建，`no-plugin` 变体无法加载。

接着在 CPA 的 `config.yaml` 中加入插件配置（如果文件里已经有 `plugins` 段，合并进去即可，不要重复写顶层键）。建议先用默认配置确认插件能正常加载，再按需调整参数：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    antigravity-prompt-bridge:
      enabled: true
      priority: 10
      mode: "always"
      ack_language: "auto"
      custom_ack_text: ""
      smart_min_bytes: 1200
      prefix_when_empty: false
      split_tool_groups: false
```

其中 `plugins.enabled` 和 `dir` 是 CPA 本身的插件总开关与目录设置；下面的 `enabled` 控制当前插件是否启用，`priority` 是 CPA 的执行优先级（示例里的 `10` 只是参考值，按你的插件链顺序调整即可）。

### 配置项说明

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `mode` | `"always"` | 移位策略。`always` 会处理所有符合条件且非空的系统提示词；`smart` 只在提示词达到字节阈值，**或**命中常见编程 Agent 特征词（如 `claude code`、`opencode`、`you are codex`、`you are omp` 等，不区分大小写）时才移位，普通短提示词保持原样。 |
| `ack_language` | `"auto"` | 合成 `model` 轮确认语的语言。`en` 固定英文，`zh` 固定中文，`auto` 根据**被挪走的系统提示词**里的汉字占比自动判断。这只影响那句占位的确认语，不影响模型正常回复的语言。 |
| `custom_ack_text` | `""` | 自定义确认语文案。去掉首尾空白后非空时会覆盖 `ack_language`；留空则使用内置文案。仅在实际插入合成轮次时生效。 |
| `smart_min_bytes` | `1200` | 仅在 `mode: "smart"` 下生效，按合并后可移位提示词的 **UTF-8 字节数**计算（不是字符数或 token 数）。填 `0` 或负数会自动回退为 `1200`。 |
| `prefix_when_empty` | `false` | 开启后，如果请求有对话内容但没有可移位的系统提示词，也会单独给 `systemInstruction` 补上 `<identity>` 前缀（不插入合成对话轮次，已有 Claude 穿插思考提示会保留）。它不会把 `smart` 模式判定无需处理的非空提示词强行挪走。 |
| `split_tool_groups` | `false` | 将 `request.tools` 中包含多个函数声明的工具组拆成多个单声明组，对齐新版官方 CLI 的请求结构。该选项独立于提示词移位，只要请求在插件处理范围内就会生效。 |

## 本地构建

需要安装 `go.mod` 指定的 Go 版本以及目标平台对应的 C 编译器（依赖 `cgo`）。Windows 下如果编译只报一句 `cgo: exit status 2`，优先检查 `PATH` 里排在前面的 MinGW-w64 或 LLVM-MinGW 架构是否匹配。

```bash
go test ./...
go vet ./...
go build -trimpath -buildvcs=false -ldflags="-s -w" -buildmode=c-shared -o dist/antigravity-prompt-bridge.dll .
python testdata/native_abi_smoke.py dist/antigravity-prompt-bridge.dll
```

## 致谢

系统提示词移位与工具组拆分的思路来源于 [StraviaPlatform](https://github.com/Stravia-AI/StraviaPlatform)。
