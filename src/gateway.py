# 本地 OpenAI Chat Completion 透明代理网关。
# 移植自 Go 版 internal/gateway/server.go。
from __future__ import annotations

import asyncio
import contextlib
import json
import logging
import os
import sys
import time
from collections.abc import Callable
from typing import Any
from urllib.parse import urlsplit

import aiohttp
from aiohttp import web

import catalog
import upstream
from cred import Credentials, NotLoggedInError
from cred import load as cred_load
from cred import refresh as cred_refresh
from portfree import bind_free_socket, display_base

logger = logging.getLogger("wbai")

# 请求体上限 32MB（与 Go 版 io.LimitReader 一致）
_MAX_BODY = 32 << 20
_READ_CHUNK = 8192  # 单次从上游读取的字节数


# 转发响应时剔除的头：hop-by-hop 头，以及与本进程实际发出内容不一致的头
# （aiohttp 会自动解压响应体，若把上游的 content-encoding 原样转发，
#   客户端会按 gzip 去解压缩后的明文而出错；content-length 同理由 aiohttp 重算）
_STRIP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "content-length",
    "content-encoding",
}


class _UpstreamUnauthorized(Exception):
    """上游拒绝了访问令牌（401/403）。"""


def host_of(base_url: str) -> str:
    """从 baseUrl 提取 host 部分。"""
    host = urlsplit(base_url).netloc
    return host or base_url.split("/")[0]


def write_json(data: Any, status: int = 200) -> web.Response:
    return web.json_response(data, status=status)


def write_json_error(status: int, code: str, message: str) -> web.Response:
    """错误响应 JSON 结构与 Go 一致：{"error":{"type","code","message"}}。"""
    return write_json({"error": {"type": "error", "code": code, "message": message}}, status)


def debug_dump(tag: str, ext: str, data: bytes) -> None:
    """把抓包数据原样落盘，目录由 WBAI_DEBUG_DUMP 指定（未设置则不启用）。"""
    directory = os.environ.get("WBAI_DEBUG_DUMP", "")
    if not directory:
        return
    name = f"{tag}-{time.strftime('%H%M%S')}-{time.monotonic_ns() % 1_000_000_000}.{ext}"
    try:
        with open(os.path.join(directory, name), "wb") as f:
            f.write(data)
    except OSError:
        pass


async def _load_credentials() -> tuple[Any, Credentials]:
    """加载上游配置与凭证；配置缺失时回退默认配置；
    凭证 domain 与配置 host 不一致时以凭证 domain 为准。"""
    try:
        # load_config 返回 (Config, ok)；磁盘缺失/不可读时返回默认配置
        cfg, _ok = upstream.load_config()
    except Exception:
        cfg = upstream.default_config()
    c = cred_load()
    if c.domain and c.domain != host_of(cfg.base_url):
        cfg.base_url = "https://" + c.domain
    return cfg, c


def _apply_auth(headers: dict[str, str], cfg: Any, token: str, uid: str) -> None:
    header = cfg.token_header or "Authorization"
    headers[header] = "Bearer " + token
    if cfg.username_header and uid:
        headers[cfg.username_header] = uid
    headers["User-Agent"] = "CodeBuddyCode/1.0"
    headers["X-Domain"] = host_of(cfg.base_url)


async def _open_stream(
    session: aiohttp.ClientSession,
    cfg: Any,
    token: str,
    uid: str,
    body: bytes,
) -> aiohttp.ClientResponse:
    """向上游发起流式请求；401/403 抛 _UpstreamUnauthorized，其他非 200 抛 RuntimeError
    （上游原始错误体只进本地日志，不回传客户端）。"""
    headers: dict[str, str] = {}
    _apply_auth(headers, cfg, token, uid)
    headers["Content-Type"] = "application/json"
    headers["Accept"] = "text/event-stream"

    try:
        resp = await session.post(upstream.chat_url(cfg), data=body, headers=headers)
    except aiohttp.ClientError as exc:
        raise RuntimeError(f"上游请求失败: {exc}") from exc

    if resp.status in (401, 403):
        snippet = (await resp.content.read(1024)).decode(errors="replace").strip()
        resp.close()
        logger.error("上游 token 被拒绝: %s", snippet)
        raise _UpstreamUnauthorized(snippet)
    if resp.status != 200:
        snippet = (await resp.content.read(2048)).decode(errors="replace").strip()
        resp.close()
        logger.error("上游返回 HTTP %d: %s", resp.status, snippet)
        raise RuntimeError(f"上游返回 HTTP {resp.status}: {snippet}")
    return resp


async def proxy_chat(request: web.Request) -> web.StreamResponse:
    if request.method != "POST":
        return write_json_error(405, "method_not_allowed", "use POST")

    # 读 body，限 32MB，超限返回 400
    chunks: list[bytes] = []
    total = 0
    while True:
        chunk = await request.content.readany()
        if not chunk:
            break
        total += len(chunk)
        if total > _MAX_BODY:
            return write_json_error(400, "bad_request", "请求体过大")
        chunks.append(chunk)
    raw = b"".join(chunks)

    try:
        req = json.loads(raw) if raw else None
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        # 非 UTF-8 请求体（如 GBK 终端直发中文）按无效 JSON 处理，不 500
        return write_json_error(400, "invalid_json", str(exc))
    if not isinstance(req, dict):
        return write_json_error(400, "invalid_json", "请求体必须是 JSON 对象")
    model = req.get("model")
    messages = req.get("messages")
    if not model or not isinstance(messages, list) or not messages:
        return write_json_error(400, "invalid_request", "model 和 messages 必填")

    verbose: bool = request.app["verbose"]
    # 对外暴露短名，内部映射回上游 slug（未知名称原样透传）
    upstream_model = catalog.resolve_model(model)
    if upstream_model != model:
        if verbose:
            logger.info("模型映射: %s -> %s", model, upstream_model)
        req["model"] = upstream_model
        raw = json.dumps(req, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    if verbose:
        logger.info("代理请求: model=%s", upstream_model)
    debug_dump("req", "json", raw)

    try:
        cfg, c = await _load_credentials()
    except NotLoggedInError as exc:
        return write_json_error(503, "not_authenticated", f"未登录: {exc}")

    session: aiohttp.ClientSession = request.app["client"]
    try:
        resp = await _open_stream(session, cfg, c.access_token, c.uid, raw)
    except _UpstreamUnauthorized:
        logger.info("token 被拒绝，尝试刷新...")
        try:
            c = await asyncio.to_thread(cred_refresh, c)
        except Exception as exc:
            logger.error("刷新失败: %s", exc)
            resp = None
        else:
            try:
                resp = await _open_stream(session, cfg, c.access_token, c.uid, raw)
            except _UpstreamUnauthorized:
                resp = None
            except RuntimeError as exc:
                # 刷新成功但第二次请求仍可能失败（上游 5xx/网络错误），不能穿透成裸 500
                logger.error("上游请求错误: %s", exc)
                return write_json_error(502, "upstream_error", "上游请求失败，请查看网关日志")
    except RuntimeError as exc:
        logger.error("上游请求错误: %s", exc)
        return write_json_error(502, "upstream_error", "上游请求失败，请查看网关日志")
    if resp is None:
        return write_json_error(
            502,
            "upstream_unauthenticated",
            "WorkBuddyAI 拒绝了 token 且刷新失败，请在网关窗口中重新登录",
        )

    # 复制上游响应头（剔除 hop-by-hop 与已失效头），状态码透传
    out = web.StreamResponse(status=resp.status)
    for name, value in resp.headers.items():
        if name.lower() not in _STRIP_HEADERS:
            out.headers.add(name, value)
    await out.prepare(request)

    # SSE 感知透传：按事件块（空行分隔）切分上游字节流，丢弃注释行（以 ":" 开头的
    # 行，如上游自带的 ": keep-alive"）后再转发。不能做纯字节透传——部分客户端的
    # SSE 解析器遇到注释行会解析失败并重置流状态，把连续的思考块切碎成多个小块。
    # 事件数据本身原样保留，不在重组时引入额外延迟。
    content_type = resp.headers.get("Content-Type", "")
    is_sse = "text/event-stream" in content_type.lower()
    capture: list[bytes] | None = [] if os.environ.get("WBAI_DEBUG_DUMP") else None
    queue: asyncio.Queue[bytes | None] = asyncio.Queue()

    async def _pump() -> None:
        try:
            while True:
                chunk = await resp.content.read(_READ_CHUNK)
                if not chunk:
                    break
                await queue.put(chunk)
        except (aiohttp.ClientError, ConnectionError) as exc:
            logger.error("读取上游响应中断: %s", exc)
        finally:
            await queue.put(None)

    pump_task = asyncio.create_task(_pump())

    def _strip_comment_lines(event: bytes) -> bytes:
        lines = event.split(b"\n")
        kept = [ln for ln in lines if not ln.startswith(b":")]
        if len(kept) == len(lines):
            return event
        return b"\n".join(kept)

    async def _forward_sse() -> None:
        buf = b""
        try:
            while True:
                chunk = await queue.get()
                if chunk is None:
                    break
                if capture is not None:
                    capture.append(chunk)
                buf += chunk
                if is_sse:
                    # 兼容 \n\n 与 \r\n\r\n 两种事件分隔符，取最先出现者
                    while True:
                        i, n = buf.find(b"\n\n"), buf.find(b"\r\n\r\n")
                        if i < 0 and n < 0:
                            break
                        if n < 0 or (0 <= i < n):
                            event, buf = buf[:i], buf[i + 2 :]
                        else:
                            event, buf = buf[:n], buf[n + 4 :]
                        cleaned = _strip_comment_lines(event)
                        if cleaned.strip():
                            # 统一规范化为标准 SSE 帧（\n\n 结尾），避免 \r 残留
                            await out.write(cleaned.rstrip(b"\r") + b"\n\n")
                else:
                    await out.write(chunk)
            if buf:
                await out.write(_strip_comment_lines(buf))
        except (ConnectionResetError, aiohttp.ClientError, ConnectionError) as exc:
            # 客户端提前断开（如主动取消请求）属正常情况，降级为调试日志
            logger.info("客户端连接已断开，停止转发: %s", exc)
        finally:
            pump_task.cancel()
            # close 而非 release：连接可能未读完，不能放回连接池复用
            resp.close()
            # 断开后 write_eof 也可能失败，忽略即可
            with contextlib.suppress(ConnectionResetError, aiohttp.ClientError):
                await out.write_eof()

    await _forward_sse()
    if capture is not None:
        debug_dump("resp", "sse", b"".join(capture))
    return out


async def handle_models(request: web.Request) -> web.Response:
    if request.method != "GET":
        return write_json_error(405, "method_not_allowed", "use GET")
    data = [
        {
            "id": short,
            "object": "model",
            "created": int(time.time()),
            "owned_by": "workbuddyai",
            "name": short,
        }
        for short in catalog.exposed_ids()
    ]
    return write_json({"object": "list", "data": data})


async def handle_health(request: web.Request) -> web.Response:
    status: dict[str, Any] = {"ok": True}
    try:
        cred_load()
    except Exception as exc:
        status["ok"] = False
        status["error"] = str(exc)
    return write_json(status)


async def handle_root(request: web.Request) -> web.Response:
    if request.path == "/":
        return write_json(
            {
                "service": "workbuddyai-gateway",
                "endpoints": [
                    "/v1/chat/completions",
                    "/v1/models",
                    "/health",
                ],
            }
        )
    return write_json_error(404, "not_found", f"unknown path {request.path}")


def make_app(verbose: bool) -> web.Application:
    app = web.Application(client_max_size=_MAX_BODY)
    app["verbose"] = verbose

    async def _on_start(app: web.Application) -> None:
        # trust_env=True 等价 Go 的 http.ProxyFromEnvironment；
        # total=None 支持无限时长流式响应，连接 5s / 读空闲 120s 与 Go 一致
        timeout = aiohttp.ClientTimeout(total=None, sock_connect=5, sock_read=120)
        app["client"] = aiohttp.ClientSession(timeout=timeout, trust_env=True)

    async def _on_cleanup(app: web.Application) -> None:
        await app["client"].close()

    app.on_startup.append(_on_start)
    app.on_cleanup.append(_on_cleanup)

    app.router.add_route("*", "/v1/chat/completions", proxy_chat)
    app.router.add_route("*", "/v1/models", handle_models)
    app.router.add_route("*", "/health", handle_health)
    app.router.add_route("*", "/", handle_root)
    # 兜底：未知路径也返回统一 JSON 404（放在最后，避免截获上面的精确路由）
    app.router.add_route("*", "/{tail:.*}", handle_root)
    return app


async def _serve_async(addr: str, verbose: bool, stop_event: asyncio.Event) -> None:
    """核心异步服务：端口自愈拿 socket → AppRunner/TCPSite → 等待停止信号。"""

    def printf(msg: str) -> None:
        print(f"[wbai] {msg}", flush=True)

    host, port = addr.rsplit(":", 1)
    host = host.strip("[]")
    sock = bind_free_socket(host, int(port), printf)

    base = display_base(addr)
    printf(f"网关已启动: http://{base}")
    printf("端点: / /v1/chat/completions /v1/models /health")

    logging.basicConfig(
        level=logging.INFO if verbose else logging.WARNING,
        format="[wbai] %(message)s",
        stream=sys.stderr,
    )

    runner = web.AppRunner(make_app(verbose))
    await runner.setup()
    site = web.SockSite(runner, sock)
    await site.start()
    try:
        await stop_event.wait()
    finally:
        await runner.cleanup()


def start_background(
    addr: str, verbose: bool, on_error: Callable[[Exception], None] | None = None
) -> Callable[[], None]:
    """在后台线程启动网关（托盘模式用），返回阻塞式 stop() 回调。

    on_error: 启动/运行期异常回调（如端口被占用），供界面展示原因。
    """
    import threading

    loop = asyncio.new_event_loop()
    stop_event = asyncio.Event()

    def _run() -> None:
        asyncio.set_event_loop(loop)
        try:
            loop.run_until_complete(_serve_async(addr, verbose, stop_event))
        except Exception as exc:  # 后台线程里的异常默认会被吞掉，这里交给调用方
            logger.error("网关线程异常退出: %s", exc)
            if on_error:
                on_error(exc)

    t = threading.Thread(target=_run, daemon=True)
    t.start()

    def stop() -> None:
        loop.call_soon_threadsafe(stop_event.set)
        t.join(timeout=6)

    return stop
