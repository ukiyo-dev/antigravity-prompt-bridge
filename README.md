# Antigravity Prompt Bridge

Antigravity Prompt Bridge 是一个 **CLIProxyAPI v8（CPA）请求规范化插件**。当客户端的 Agent 系统提示词随请求发往 Antigravity 时，插件可以保留提示词内容，只改变它在请求中的位置。

**目的：通过调整发往 Antigravity 的系统提示词结构，规避可能由请求结构触发的 HTTP 429 风控。** 
## 工作原理

插件注册为 CPA 的 C ABI `RequestNormalizer`，在 `request.normalize` 阶段处理 **已转换为 Antigravity 格式**的请求，而不是拦截客户端的原始 JSON。例如，OpenAI Chat Completions 的 `system` / `developer` 消息先由 CPA 转换，然后才由插件处理。

命中移位条件时，请求结构从左侧变为右侧（省略其他字段）：

| 字段 | 转换前 | 转换后 |
| --- | --- | --- |
| `request.systemInstruction` | Agent 系统提示词 | Antigravity 的 296 字节 `<identity>` 前缀 + 桥接说明；CPA 的 Claude 穿插思考提示（如有）仍保留在这里 |
| `request.contents` | 原始对话历史 | 合成 `user`（移入的系统提示词）→ 合成 `model`（确认语）→ 原始对话历史 |
| `request.sessionId` | 可能缺失 | 保留已有值；缺失时，在移位前从原始对话中找到首个 `user` 文本片段，计算 FNV‑1a 64 位哈希 |

桥接说明要求模型把插入的第一条 `user` 消息当作会话指令，而把后续真正的用户消息当作任务。**移位后的提示词位于普通对话历史，不再是原生系统指令**；合成确认语也是适配器写入的，不是模型调用的结果。原始对话轮次的顺序和内容不变。

`sessionId` 在移位前计算，是为了避免不同会话都因同一份静态提示词被插在最前面而得到相同的标识。它不是全局唯一 ID：相同的首段用户文本会产生相同的回退值；若 CPA 已从显式会话头派生出会话 ID，则优先使用后者。没有可用的用户文本时，插件不生成这个回退值。

**排除范围：** 非 Antigravity 请求、明确标记为非 Agent 的请求、图像模型，以及带原生搜索工具但没有函数声明的请求不处理。没有非空 `contents` 数组、已带相同身份前缀的请求，以及专用搜索机器人提示词不做提示词移位。可选的工具拆组与提示词移位相互独立。

## 安装与配置

在 [GitHub Releases](https://github.com/ukiyo-dev/antigravity-prompt-bridge/releases) 下载与 **CPA 进程**的系统和架构匹配的 ZIP（Windows / Linux / macOS，`amd64` / `arm64`）。用 Release 的 `checksums.txt` 校验 ZIP，将根目录的 `.dll`、`.so` 或 `.dylib` 解压到 CPA 的插件目录。Linux 需要支持动态插件的 CPA，不能使用 `no-plugin` 变体。

将以下片段合并到 CPA 的 `config.yaml`；已有 `plugins` 段时，不要重复添加顶层键。建议先保留默认策略，确认插件已注册，再按需要调整。

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

`plugins.enabled` 是 CPA 的插件系统总开关，`dir` 指定插件目录。实例级 `enabled` 控制此插件是否启用，`priority` 是 CPA 的调度字段；示例中的 `10` 不是桥接插件的必需值。

### 插件字段

| 字段 | 类型 | 默认值 | 行为 |
| --- | --- | --- | --- |
| `mode` | 枚举 | `always` | `always`：对符合条件、存在可移位内容的请求执行移位。`smart`：只有合并后的可移位文本达到字节阈值，**或**命中 Agent 关键词时才移位；否则保留原生系统指令。关键词包含 `claude code`、`you are codex`、`you are omp`、`opencode` 等，不区分大小写。 |
| `ack_language` | 枚举 | `auto` | 控制合成确认语。`en` 固定英文，`zh` 固定中文；`auto` 根据**被移位的提示词文本**中汉字占字母的比例选择中文，否则使用英文。它不是模型正常回复的语言设置。 |
| `custom_ack_text` | 字符串 | `""` | 去掉首尾空白后若非空，直接作为合成确认语，优先于 `ack_language`。留空则使用内置确认语；仅在实际插入合成轮次时生效。 |
| `smart_min_bytes` | 整数 | `1200` | 只在 `mode: smart` 下生效。合并后的可移位提示词文本按 **UTF‑8 字节**计数，达到阈值即命中；不是字符数或 token 数。设为 `0` 或负数会恢复为 `1200`。 |
| `prefix_when_empty` | 布尔 | `false` | 对有原始对话、但没有可移位系统内容的请求，可仅补入 `<identity>` 前缀，不增加合成 `user` / `model` 轮次；保留适用的 Claude 穿插思考提示。它不会强制移位 `smart` 未命中的非空提示词。 |
| `split_tool_groups` | 布尔 | `false` | 把 `request.tools` 中含多个函数声明的一组拆成多个单声明组。即使没有发生提示词移位，只要请求属于插件处理范围，仍可执行拆组。 |

## 开发与验证

使用 `go.mod` 指定的 Go 版本和目标平台的 C 编译器。Windows 上若 `cgo` 只报 `exit status 2`，先检查正确架构的 MinGW-w64 / LLVM-MinGW 是否排在 `PATH` 前面。

```text
go test ./...
go vet ./...
go build -trimpath -buildvcs=false -ldflags="-s -w" -buildmode=c-shared -o dist/antigravity-prompt-bridge.dll .
python testdata/native_abi_smoke.py dist/antigravity-prompt-bridge.dll
```
