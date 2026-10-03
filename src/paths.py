"""路径解析：workbuddyai-gateway 全部文件位置。

布局（均在用户主目录下）：
    ~/.workbuddyai-gateway/
        credentials.json   访问/刷新令牌 + uid + domain
        upstream.json      已解析的上游端点与鉴权头名称
        prefs.json         界面偏好（主题模式等）
        gateway.log        运行日志（cli 写入）
"""

from __future__ import annotations

import os
from pathlib import Path

# 用户主目录下的存储目录名
DIR_NAME = ".workbuddyai-gateway"


def home() -> Path:
    """返回用户主目录；WBAI_HOME 可覆盖（便携模式/测试用）。"""
    override = os.environ.get("WBAI_HOME")
    if override:
        return Path(override)
    return Path(os.path.expanduser("~"))


def base_dir() -> Path:
    """返回 ~/.workbuddyai-gateway（不创建）。"""
    return home() / DIR_NAME


def ensure_dir() -> Path:
    """返回 ~/.workbuddyai-gateway，缺失时创建。"""
    d = base_dir()
    d.mkdir(parents=True, exist_ok=True)
    return d


def credentials_path() -> Path:
    """凭证文件路径。"""
    return base_dir() / "credentials.json"


def upstream_path() -> Path:
    """上游配置文件路径。"""
    return base_dir() / "upstream.json"


def prefs_path() -> Path:
    """界面偏好（主题模式等）文件路径。"""
    return base_dir() / "prefs.json"
