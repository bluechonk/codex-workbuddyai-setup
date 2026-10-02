# codex-workbuddyai-setup

把 [Codex CLI](https://github.com/openai/codex) 接到 WorkBuddyAI 上。

Codex 只说 OpenAI **Responses API**，而 WorkBuddyAI 提供的是 **chat completions**，
本项目补上中间那一层：本地网关做双向协议转换，`wbai` CLI 负责登录、生成模型
目录、写入 `~/.codex/config.toml`。

## 快速开始

从 [Releases](https://github.com/bluechonk/codex-workbuddyai-setup/releases) 下载
`codex-workbuddyai-setup-<版本>.exe`，双击进菜单；或自己构建：

```bash
go build -o wbai.exe ./cmd/wbai

wbai setup      # 登录 → 安装内置模型目录 → 改 ~/.codex/config.toml（跑一次）
wbai serve      # 启动网关，默认 127.0.0.1:8787（用 WorkBuddyAI 期间保持运行）
codex           # 正常用
```

## 命令

| 命令 | 作用 |
| --- | --- |
| `wbai setup` | 登录 + 模型目录 + config.toml，一键全链路 |
| `wbai serve [--addr] [--verbose]` | 启动本地网关（常驻） |
| `wbai models [--source auto/cache/api] [--all] [--only a,b]` | 可选：从上游重新拉取并重建模型目录（日常不用） |
| `wbai config [--addr] [--model]` | 只修补 `~/.codex/config.toml` |
| `wbai login [--force]` | 浏览器登录 |
| `wbai doctor` | 自检整条链路 |

## 存储位置

```
~/.codex-workbuddyai-setup/   credentials.json / upstream.json / config.json（偏好）
~/.codex/
├── config.toml               托管段（先备份）
└── workbuddyai-models.json   模型目录，setup 时从二进制内置副本覆盖安装
```

模型目录的正本随仓库分发（`cmd/wbai/workbuddyai-models.json`，go:embed 内嵌进
二进制），`setup` 不请求上游、直接覆盖安装；上游模型阵容变化时才用 `wbai models`
重新拉取重建。

## 关键机制

- **模型列表有两个上游来源**：桌面应用缓存（约 29 个）比 CLI 接口（约 18 个）
  更全。`wbai models`（可选刷新路径）以缓存为底、接口覆盖元数据取并集。
- **上游三个硬性约束**：不支持非流式（网关恒发 stream=true，非流式由网关聚合）；
  首条消息必须是 system；聊天路径是 `/v2/chat/completions`。
- **config.toml 是行级补丁**，不是整体覆盖：只删自己拥有的键和 provider 表，
  其余原样保留；写盘原子化，重复顶层键拒绝写入。
- **custom/freeform 工具**（如 apply_patch）降级为 function 工具发给上游，
  响应侧重构回 `custom_tool_call`；上游断流时向 Codex 发 `response.failed` 终态。

细节见 `internal/gateway`、`internal/codexcfg` 的代码注释。

## 开发

```bash
go build ./... && go vet ./... && go test ./...
```

零第三方依赖。发版与提交规范见 [AGENTS.md](AGENTS.md)。

## 注意

- 网关必须处于运行状态 Codex 才能工作，目前未托管成系统服务。
- `~/.codex-workbuddyai-setup/credentials.json` 含 access token（0600），不要提交。
