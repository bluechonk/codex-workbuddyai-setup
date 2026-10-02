# AGENTS.md

## 发版

- 产物命名固定为 `codex-workbuddyai-setup-<版本>.exe`（仅 windows/amd64，不做其他平台）。
  构建命令：`go build -trimpath -ldflags "-s -w -X main.version=<版本>" -o codex-workbuddyai-setup-<版本>.exe ./cmd/wbai`
- 发版流程：打 tag `v<版本>` 并推送 → `gh release create v<版本> <exe>`，Release Notes 附 SHA256。

## 提交规范

提交信息格式为 `<type>: 中文描述`，正文用中文说明改动原因与影响。

```
feat: 模型列表优先读取桌面应用缓存并支持筛选
fix: 修正 config.toml 根键被写入错误表导致配置不生效
docs: 补充提交信息规范
```

常用 type：`feat` 新功能、`fix` 缺陷修复、`docs` 文档、`refactor` 重构、`test` 测试、`chore` 杂项。一次提交只做一件事，不要把功能和修复混在一起。
