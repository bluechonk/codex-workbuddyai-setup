# 模型目录与别名映射。
# 对客户端与 UI 暴露短名（如 deepseek-flash），转发上游时映射回真实 slug。
# 配置来源是 models.json：
#   - 每条模型的 slug 字段是上游真实模型名
#   - 可选 alias（或 short）字段指定对外短名；未提供时用内置 ALIASES 兜底
# 文件缺失 / 损坏时使用内置默认，不阻塞启动。
from __future__ import annotations

import json
import os
import sys
import threading
from pathlib import Path

# 内置兜底映射：短名 → 上游 slug
ALIASES: dict[str, str] = {
    "deepseek-flash": "deepseek-v4.1-flash",
}

# models.json 中可用的短名字段名（按优先级）
_ALIAS_FIELDS = ("alias", "short", "shortName", "exposed_id")

# 目录缓存：按 (文件路径, mtime, 大小) 失效，避免每个请求都读盘
_cache_lock = threading.Lock()
_cache_key: tuple[str, float, int] | None = None
_cache_catalog: list[tuple[str, str]] = []


def _candidate_paths() -> list[Path]:
    """models.json 的查找顺序：环境变量 → exe 同目录/打包内嵌 → 工作目录 → 项目根。"""
    paths: list[Path] = []
    env = os.environ.get("WBAI_MODELS_FILE")
    if env:
        paths.append(Path(env))
    if getattr(sys, "frozen", False):
        paths.append(Path(sys.executable).parent / "models.json")
        meipass = getattr(sys, "_MEIPASS", None)
        if meipass:
            paths.append(Path(meipass) / "models.json")
    paths.append(Path.cwd() / "models.json")
    paths.append(Path(__file__).resolve().parent.parent / "models.json")  # 项目根（源码运行）
    return paths


def _parse_catalog_file(path: Path) -> list[tuple[str, str]]:
    """解析单个 models.json，返回 (目录项)。短名优先取模型条目里的 alias 字段；
    没有 alias 时用内置 ALIASES 按 slug 反查。两者都没有则忽略该模型。"""
    reverse = {slug: short for short, slug in ALIASES.items()}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return []
    entries: list[tuple[str, str]] = []
    seen: set[str] = set()
    models = data.get("models") if isinstance(data, dict) else None
    for model in models or []:
        if not isinstance(model, dict):
            continue
        slug = model.get("slug")
        if not slug:
            continue
        slug = str(slug)
        short = ""
        for field in _ALIAS_FIELDS:
            if isinstance(model.get(field), str) and model[field].strip():
                short = model[field].strip()
                break
        if not short:
            short = reverse.get(slug, "")
        if not short or short in seen:
            continue
        seen.add(short)
        entries.append((short, slug))
    return entries


def load_catalog() -> list[tuple[str, str]]:
    """返回 [(短名, 上游 slug)]；缓存键为 (路径, mtime, 大小)。

    先 stat 比对缓存键，未变化直接返回缓存（不读盘不解析）；
    变化/首次才读盘解析。文件缺失/损坏、或文件里没有任何可用模型时，退回内置 ALIASES。
    """
    global _cache_key, _cache_catalog
    for path in _candidate_paths():
        try:
            stat = path.stat()
        except OSError:
            continue
        key = (str(path), stat.st_mtime, stat.st_size)
        with _cache_lock:
            if key == _cache_key:
                return list(_cache_catalog)
        entries = _parse_catalog_file(path)
        with _cache_lock:
            _cache_key = key
            _cache_catalog = entries or list(ALIASES.items())
            return list(_cache_catalog)
    return list(ALIASES.items())


def exposed_ids() -> list[str]:
    """对外暴露的模型 ID 列表（短名）。"""
    return [short for short, _slug in load_catalog()]


def resolve_model(name: str) -> str:
    """把客户端请求中的短名映射为上游 slug；未知名称原样透传。"""
    for short, slug in load_catalog():
        if short == name:
            return slug
    return name
