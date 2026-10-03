"""与 WorkBuddyAI API 通信：拉取模型目录、解析连接配置。"""

from __future__ import annotations

import contextlib
import json
import os
from dataclasses import asdict, dataclass
from typing import Any

import requests

import paths
from cred import DEFAULT_BASE_URL as DEFAULT_BASE_URL  # noqa: F401

# 已验证的 chat-completions 路由。模型载荷虽然声明了
# authentication.attributes.prefixPath（"/plugin"），但
# POST /plugin/v2/chat/completions 返回 404，POST /v2/chat/completions
# 返回 200，因此刻意不使用该前缀。
DEFAULT_CHAT_PATH = "/v2/chat/completions"


class UpstreamUnauthorized(Exception):
    """上游 API 拒绝了访问令牌（HTTP 401/403）。"""


@dataclass
class Config:
    """描述如何到达 WorkBuddyAI chat 端点。JSON 字段名与 Go 版一致。"""

    base_url: str = ""
    chat_path: str = ""
    token_header: str = ""
    token_type: str = ""
    username_header: str = ""

    _FIELDS = (
        ("base_url", "baseUrl"),
        ("chat_path", "chatPath"),
        ("token_header", "tokenHeader"),
        ("token_type", "tokenType"),
        ("username_header", "usernameHeader"),
    )

    def to_dict(self) -> dict[str, str]:
        d = asdict(self)
        return {json_name: d[py_name] for py_name, json_name in self._FIELDS}

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Config:
        kwargs = {py: data.get(json_name) for py, json_name in cls._FIELDS}
        return cls(**kwargs)  # type: ignore[arg-type]


def default_config() -> Config:
    """尚未拉取模型载荷时使用的连接配置。"""
    return Config(
        base_url="https://www.workbuddy.ai",
        chat_path=DEFAULT_CHAT_PATH,
        token_header="Authorization",
        token_type="bearerToken",
        username_header="X-User-Id",
    )


def load_config() -> tuple[Config, bool]:
    """读取已解析的上游描述符。

    返回 (配置, 是否来自磁盘)。upstream.json 缺失时返回 (default_config(), False)，
    由调用方按"默认值生效"处理；文件存在但损坏时抛 ValueError。
    """
    path = paths.upstream_path()
    try:
        raw = path.read_bytes()
    except FileNotFoundError:
        return default_config(), False
    try:
        cfg = Config.from_dict(json.loads(raw))
    except (json.JSONDecodeError, UnicodeDecodeError) as e:
        raise ValueError(f"parse upstream config: {e}") from e
    if not cfg.base_url:
        cfg.base_url = default_config().base_url
    if not cfg.chat_path:
        cfg.chat_path = DEFAULT_CHAT_PATH
    return cfg, True


def save_config(cfg: Config) -> None:
    """持久化已解析的上游描述符（临时文件 + 原子替换，避免半截文件）。"""
    paths.ensure_dir()
    path = paths.upstream_path()
    pretty = json.dumps(cfg.to_dict(), indent=2, ensure_ascii=False)
    tmp = path.with_suffix(".json.tmp")
    tmp.write_text(pretty + "\n", encoding="utf-8")
    os.replace(tmp, path)
    with contextlib.suppress(OSError):
        path.chmod(0o600)


def _trim_slash(s: str) -> str:
    return s.rstrip("/")


def _ensure_slash(s: str) -> str:
    if not s:
        return "/"
    return s if s.startswith("/") else "/" + s


def chat_url(cfg: Config) -> str:
    """完整限定的 chat-completions URL。"""
    return _trim_slash(cfg.base_url) + _ensure_slash(cfg.chat_path)


def models_url(cfg: Config) -> str:
    """列出可用模型的端点。"""
    return _trim_slash(cfg.base_url) + "/v2/enterprises/personal/models"


def fetch_models(cfg: Config, token: str, uid: str) -> dict[str, Any]:
    """拉取并解码给定令牌对应的模型载荷，返回其中的 data 字典。"""
    headers = {
        "Authorization": "Bearer " + token,
        "Accept": "application/json",
        "User-Agent": "CodeBuddyCode/1.0",
    }
    if uid:
        headers["X-User-Id"] = uid
    try:
        resp = requests.get(models_url(cfg), headers=headers, timeout=60)
    except requests.RequestException as e:
        raise RuntimeError(f"models request failed: {e}") from e
    if resp.status_code in (401, 403):
        raise UpstreamUnauthorized("upstream rejected the access token (HTTP 401/403)")
    if resp.status_code != 200:
        raise RuntimeError(f"models request failed: HTTP {resp.status_code}")
    try:
        env = resp.json()
    except ValueError as e:
        raise RuntimeError(f"models response is not valid JSON: {e}") from e
    data = env.get("data")
    if not data:
        raise RuntimeError(
            f'models response has no "data" field (code={env.get("code")} msg={env.get("msg", "")})'
        )
    if not isinstance(data, dict):
        raise RuntimeError("decode models payload: data is not an object")
    if not data.get("models"):
        raise RuntimeError("models payload contains no models")
    return data


def resolve_config(data: dict[str, Any], fallback: Config) -> Config:
    """从模型载荷推导连接配置。"""
    cfg = Config(**asdict(fallback))
    endpoint = data.get("endpoint")
    if endpoint:
        cfg.base_url = endpoint
    cfg.chat_path = DEFAULT_CHAT_PATH
    auth = data.get("authentication") or {}
    attrs = auth.get("attributes") or {}
    if attrs.get("tokenHeader"):
        cfg.token_header = attrs["tokenHeader"]
    if attrs.get("tokenType"):
        cfg.token_type = attrs["tokenType"]
    if attrs.get("usernameHeader"):
        cfg.username_header = attrs["usernameHeader"]
    return cfg
