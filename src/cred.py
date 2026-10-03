"""认证状态：浏览器设备登录流程、凭证持久化与访问令牌刷新。"""

from __future__ import annotations

import base64
import contextlib
import json
import os
import time
import webbrowser
from collections.abc import Callable
from dataclasses import asdict, dataclass
from typing import Any

import requests

import paths

# 凭证无显式 domain 时使用的默认域名
DEFAULT_BASE_URL = "https://www.workbuddy.ai"
# 向 WorkBuddyAI 插件端点标识本客户端
PLATFORM_QS = "platform=workbuddy"

_LOGIN_WINDOW = 5 * 60  # 登录等待窗口（秒）
_POLL_INTERVAL = 5  # 轮询间隔（秒）
_MAX_LOGIN_ROUNDS = 2  # 整轮登录重试次数
_HTTP_TIMEOUT = 30  # HTTP 超时（秒）

_NOT_LOGGED_IN_MSG = "no credentials found; run `wbai` to log in and start"


class NotLoggedInError(Exception):
    """磁盘上不存在可用凭证。"""


@dataclass
class Credentials:
    """credentials.json 的磁盘布局。JSON 字段名与 Go 版一致。"""

    access_token: str = ""
    refresh_token: str = ""
    token_type: str = ""
    scope: str = ""
    session_state: str = ""
    domain: str = ""
    uid: str = ""
    expires_in: str = ""
    expires_at: Any = None
    refresh_expires_in: str = ""
    refresh_expires_at: Any = None
    obtained_at: str = ""
    source: str = ""

    _FIELDS = (
        ("access_token", "accessToken"),
        ("refresh_token", "refreshToken"),
        ("token_type", "tokenType"),
        ("scope", "scope"),
        ("session_state", "sessionState"),
        ("domain", "domain"),
        ("uid", "uid"),
        ("expires_in", "expiresIn"),
        ("expires_at", "expiresAt"),
        ("refresh_expires_in", "refreshExpiresIn"),
        ("refresh_expires_at", "refreshExpiresAt"),
        ("obtained_at", "obtainedAt"),
        ("source", "source"),
    )

    def to_dict(self) -> dict[str, Any]:
        """转为 Go JSON 字段名布局的字典。"""
        d = asdict(self)
        return {json_name: d[py_name] for py_name, json_name in self._FIELDS}

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Credentials:
        """从 Go JSON 字段名布局的字典构造。"""
        kwargs = {py: data.get(json_name) for py, json_name in cls._FIELDS}
        return cls(**kwargs)  # type: ignore[arg-type]

    def domain_or(self, default_domain: str) -> str:
        """返回 API 域名，为空时回退到编译期默认值。"""
        return self.domain if self.domain else default_domain


def load() -> Credentials:
    """从磁盘读取凭证。

    文件缺失 / JSON 损坏 / accessToken 为空时一律抛 NotLoggedInError
    （Go 版损坏时抛其他错误，Python 版按主线决策统一归为未登录）。
    uid 为空时从 JWT sub 提取补上。
    """
    path = paths.credentials_path()
    try:
        raw = path.read_bytes()
    except FileNotFoundError:
        raise NotLoggedInError(_NOT_LOGGED_IN_MSG) from None
    try:
        data = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError):
        raise NotLoggedInError(_NOT_LOGGED_IN_MSG) from None
    if not isinstance(data, dict):
        # 文件内容是合法 JSON 但不是对象（如 []、数字、字符串）→ 同样视为未登录
        raise NotLoggedInError(_NOT_LOGGED_IN_MSG)
    c = Credentials.from_dict(data)
    if not c.access_token:
        raise NotLoggedInError(_NOT_LOGGED_IN_MSG)
    if not c.uid:
        c.uid = _jwt_sub(c.access_token)
    return c


def save(c: Credentials) -> None:
    """以 0600 权限写入凭证文件（Windows 上 chmod 意义有限，仍保持一致）。

    先写临时文件再原子替换：写入过程崩溃不会留下半截 JSON 导致凭证损坏。
    """
    paths.ensure_dir()
    path = paths.credentials_path()
    pretty = json.dumps(c.to_dict(), indent=2, ensure_ascii=False)
    tmp = path.with_suffix(".json.tmp")
    tmp.write_text(pretty + "\n", encoding="utf-8")
    os.replace(tmp, path)  # 原子替换（Windows/Linux 均可用）
    # Windows 上 chmod 意义有限，仍调用保持一致
    with contextlib.suppress(OSError):
        path.chmod(0o600)


def from_token(
    access: str,
    refresh: str,
    token_type: str,
    scope: str,
    session: str,
    domain: str,
) -> Credentials:
    """把新鲜令牌载荷转换为可存储凭证。"""
    return Credentials(
        access_token=access,
        refresh_token=refresh,
        token_type=token_type,
        scope=scope,
        session_state=session,
        domain=domain,
        uid=_jwt_sub(access),
        obtained_at=_rfc3339_now(),
        source="workbuddy-login-poll",
    )


def _rfc3339_now() -> str:
    """当前 UTC 时间的 RFC3339 字符串（与 Go RFC3339Nano 对齐，含 Z 后缀）。"""
    t = time.time()
    frac = t - int(t)
    base = time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(t))
    return f"{base}.{int(frac * 1e9):09d}Z"


def _jwt_sub(token: str) -> str:
    """从 JWT 载荷提取 sub 声明；不可用时返回空串。"""
    parts = token.split(".")
    if len(parts) < 2:
        return ""
    try:
        decoded = base64.urlsafe_b64decode(parts[1] + "=" * (-len(parts[1]) % 4))
        claims = json.loads(decoded)
    except Exception:
        return ""
    sub = claims.get("sub") if isinstance(claims, dict) else None
    return sub if isinstance(sub, str) else ""


def _anon_headers() -> dict[str, str]:
    """把请求标记为匿名（未鉴权）的插件端点头。"""
    return {
        "X-No-Authorization": "true",
        "X-No-User-Id": "true",
        "X-No-Enterprise-Id": "true",
        "X-No-Department-Info": "true",
    }


def login(
    base_url: str,
    on_url: Callable[[str], None] | None = None,
    on_status: Callable[[str], None] | None = None,
) -> Credentials:
    """运行交互式浏览器登录并持久化结果。

    on_url: 拿到授权链接时回调（界面可展示/复制）
    on_status: 进度回调（等待授权、超时重试等）
    """
    if not base_url:
        base_url = DEFAULT_BASE_URL
    data = _login_flow(base_url, on_url=on_url, on_status=on_status)
    c = from_token(
        data.get("accessToken", ""),
        data.get("refreshToken", ""),
        data.get("tokenType", ""),
        data.get("scope", ""),
        data.get("sessionState", ""),
        data.get("domain", ""),
    )
    if not c.domain:
        c.domain = _host_of(base_url)
    c.expires_in = _any_to_str(data.get("expiresIn"))
    c.refresh_expires_in = _any_to_str(data.get("refreshExpiresIn"))
    now = time.time()
    c.expires_at = _unix_after(now, data.get("expiresIn"))
    c.refresh_expires_at = _unix_after(now, data.get("refreshExpiresIn"))
    save(c)
    return c


def refresh(c: Credentials) -> Credentials:
    """用存储的刷新令牌换取新的访问令牌。失败由调用方回退到交互登录。"""
    if c is None or not c.refresh_token:
        raise ValueError("no refresh token available")
    base = f"https://{c.domain}" if c.domain else DEFAULT_BASE_URL
    url = f"{base}/v2/plugin/auth/refresh?{PLATFORM_QS}"
    resp = requests.post(
        url,
        json={"refreshToken": c.refresh_token},
        headers={**_anon_headers(), "Content-Type": "application/json"},
        timeout=_HTTP_TIMEOUT,
    )
    if resp.status_code != 200:
        raise RuntimeError(f"refresh failed: HTTP {resp.status_code}")
    try:
        env = resp.json()
    except ValueError as e:
        raise RuntimeError(f"refresh: invalid JSON: {e}") from e
    if env.get("code") != 0 or not env.get("data"):
        raise RuntimeError(f"refresh failed: code={env.get('code')} msg={env.get('msg', '')}")
    t = env["data"]
    if not t.get("accessToken"):
        raise ValueError("refresh returned an empty access token")

    def _or(new: Any, old: str) -> str:
        v = new if isinstance(new, str) else _any_to_str(new)
        return v if v else old

    next_c = from_token(
        t["accessToken"],
        _or(t.get("refreshToken"), c.refresh_token),
        _or(t.get("tokenType"), c.token_type),
        _or(t.get("scope"), c.scope),
        _or(t.get("sessionState"), c.session_state),
        _or(t.get("domain"), c.domain),
    )
    now = time.time()
    next_c.expires_in = _any_to_str(t.get("expiresIn"))
    next_c.refresh_expires_in = _any_to_str(t.get("refreshExpiresIn"))
    next_c.expires_at = _unix_after(now, t.get("expiresIn"))
    next_c.refresh_expires_at = _unix_after(now, t.get("refreshExpiresIn"))
    if not next_c.uid:
        next_c.uid = c.uid
    save(next_c)
    return next_c


def _login_flow(
    base_url: str,
    on_url: Callable[[str], None] | None = None,
    on_status: Callable[[str], None] | None = None,
) -> dict[str, Any]:
    """整轮创建登录链接 / 轮询令牌，最多重试 _MAX_LOGIN_ROUNDS 次。"""

    def status(msg: str) -> None:
        print(msg)
        if on_status:
            on_status(msg)

    for round_no in range(1, _MAX_LOGIN_ROUNDS + 1):
        state = _create_login_state(base_url)
        auth_url = state.get("authUrl", "")
        if not auth_url:
            auth_url = f"{base_url}/login?{PLATFORM_QS}&state={_url_escape(state['state'])}"
        print()
        print(f"=== Login attempt {round_no}/{_MAX_LOGIN_ROUNDS} ===")
        print(f"Login URL: {auth_url}")
        if on_url:
            on_url(auth_url)
        status(
            f"第 {round_no}/{_MAX_LOGIN_ROUNDS} 轮：已打开浏览器授权页，"
            f"等待授权中（最多 {_LOGIN_WINDOW // 60} 分钟）…"
        )
        _open_browser(auth_url)
        data = _poll_token(base_url, state["state"])
        if data is not None:
            return data
        status(f"第 {round_no} 轮等待超时，准备重试…")
    raise RuntimeError(f"no login detected after {_MAX_LOGIN_ROUNDS} attempt(s)")


def _create_login_state(base_url: str) -> dict[str, Any]:
    """向服务端申请登录 state 与授权链接。"""
    url = f"{base_url}/v2/plugin/auth/state?{PLATFORM_QS}"
    resp = requests.post(
        url,
        data="{}",
        headers={**_anon_headers(), "Content-Type": "application/json"},
        timeout=_HTTP_TIMEOUT,
    )
    if resp.status_code != 200:
        raise RuntimeError(f"create login link: HTTP {resp.status_code}")
    try:
        env = resp.json()
    except ValueError as e:
        raise RuntimeError(f"create login link: invalid JSON: {e}") from e
    if env.get("code") != 0 or not env.get("data"):
        raise RuntimeError(
            f"failed to create login link: code={env.get('code')} msg={env.get('msg', '')}"
        )
    st = env["data"]
    if not st.get("state"):
        raise ValueError("login state is empty")
    return st


def _poll_token(base_url: str, state: str) -> dict[str, Any] | None:
    """轮询令牌端点直至截止时间；超时返回 None。"""
    url = f"{base_url}/v2/plugin/auth/token?state={_url_escape(state)}"
    deadline = time.time() + _LOGIN_WINDOW
    while time.time() < deadline:
        try:
            resp = requests.get(url, headers=_anon_headers(), timeout=_HTTP_TIMEOUT)
            if resp.status_code == 200:
                try:
                    env = resp.json()
                except ValueError:
                    env = None
                if env and env.get("code") == 0 and env.get("data"):
                    td = env["data"]
                    if td.get("accessToken"):
                        return td
        except requests.RequestException:
            pass
        time.sleep(_POLL_INTERVAL)
    return None


def _open_browser(url: str) -> None:
    """在系统默认浏览器中打开 url（不阻塞）。"""
    try:
        if not webbrowser.open(url):
            raise RuntimeError("webbrowser.open returned False")
    except Exception as e:
        print(f"Could not open the browser automatically: {e}")
        print(f"Open this URL manually: {url}")


def _host_of(base_url: str) -> str:
    """从 base URL 提取主机名。"""
    s = base_url
    if "://" in s:
        s = s.split("://", 1)[1]
    for ch in "/?":
        i = s.find(ch)
        if i >= 0:
            s = s[:i]
    return s


def _any_to_str(v: Any) -> str:
    """任意 JSON 值转字符串；None 转空串。"""
    if v is None:
        return ""
    if isinstance(v, str):
        return v
    return str(v)


def _unix_after(start: float, d: Any) -> int | None:
    """返回 start 之后 d 秒的 unix 时间戳；无法解析返回 None。"""
    if isinstance(d, (int, float)) and not isinstance(d, bool):
        sec = float(d)
    elif isinstance(d, str):
        try:
            sec = float(d)
        except ValueError:
            return None
    else:
        return None
    return int(start + sec)


def _url_escape(s: str) -> str:
    """查询参数值用的百分号转义（字母数字 -_.~ 之外转 %XX）。"""
    out = []
    for b in s.encode("utf-8"):
        c = chr(b)
        if c.isalnum() and ord(c) < 128 or c in "-_.~":
            out.append(c)
        else:
            out.append(f"%{b:02X}")
    return "".join(out)
