# 命令行入口：无参数 = 一体式启动（凭证检查 → 登录探测 → 启动网关）。
import argparse
import contextlib
import sys

__version__ = "1.0.0"

import catalog
from cred import DEFAULT_BASE_URL, NotLoggedInError, load, login, refresh
from gateway import run as gateway_run
from portfree import display_base
from tray import run_gui as tray_run
from upstream import (
    UpstreamUnauthorized,
    default_config,
    fetch_models,
    load_config,
    resolve_config,
    save_config,
)

DEFAULT_ADDR = "127.0.0.1:8787"


def print_banner(addr: str) -> None:
    """打印可直接复制到其他客户端的网关配置。"""
    base = display_base(addr)
    print(f"WorkBuddyAI Gateway v{__version__}（一体式启动）")
    print("---- 复制以下配置到你的 OpenAI 兼容客户端 ----")
    print(f"Base URL:  http://{base}/v1")
    print(f"端点:      http://{base}/v1/chat/completions")
    print("API Key:   任意非空值（网关忽略，认证走 WorkBuddyAI token）")
    print(f"模型列表:  http://{base}/v1/models")
    print(f"可用模型:  {', '.join(catalog.exposed_ids())}")
    print(f"健康检查:  http://{base}/health")
    print("----------------------------------------------")


def _config_for(cred) -> "object":
    """返回按凭证域名修正过的上游配置（文件缺失时用默认值）。

    load_config 返回 (Config, ok)：磁盘缺失/不可读时 Config 已是默认值。
    """
    try:
        cfg, _ok = load_config()
    except Exception:
        cfg = default_config()
    if cred is not None and getattr(cred, "domain", ""):
        host = cfg.base_url.split("://", 1)[-1].split("/?", 1)[0].split("/", 1)[0]
        if cred.domain != host:
            cfg.base_url = "https://" + cred.domain
    return cfg


def ensure_ready(force_login: bool) -> None:
    """启动网关前保证存在可用登录凭证：
    无凭证/损坏 → 浏览器登录；有凭证 → 探测登录状态，
    token 失效先刷新，刷新仍被拒则重新登录；网络失败警告后照常启动。
    """
    if force_login:
        print("已指定 --force-login，重新登录...")
        fresh_login()
        return

    try:
        c = load()
    except NotLoggedInError:
        print("未发现登录凭证，开始浏览器登录...")
        fresh_login()
        return
    except Exception as exc:
        # 凭证文件不可读（权限/IO 等）不应直接终止，按未登录处理并给出原因
        print(f"凭证读取失败（{exc}），开始浏览器登录...")
        fresh_login()
        return

    print(f"已加载凭证 (UID: {c.uid})，正在验证登录状态...")
    cfg = _config_for(c)
    try:
        fetch_models(cfg, c.access_token, c.uid)
    except UpstreamUnauthorized:
        print("token 已失效，尝试刷新...")
        try:
            c = refresh(c)
            cfg = _config_for(c)
            fetch_models(cfg, c.access_token, c.uid)
        except Exception as exc:  # 刷新失败或仍被拒 → 重新登录
            print(f"刷新后 token 仍被拒绝（{exc}），需要重新登录...")
            fresh_login()
            return
    except Exception as exc:
        # 网络不通等临时性问题不阻塞启动，运行期请求失败会自动刷新 token
        print(f"警告: 登录状态验证未通过（{exc}），仍将启动网关。")
        return
    print("登录状态正常。")


def fresh_login() -> None:
    """浏览器登录，并用新 token 拉取模型列表刷新上游配置。"""
    c = login(DEFAULT_BASE_URL)
    print(f"登录成功，UID: {c.uid}，域名: {c.domain}")
    cfg = _config_for(c)
    try:
        data = fetch_models(cfg, c.access_token, c.uid)
        save_config(resolve_config(data, cfg))
    except Exception as exc:
        print(f"警告: 拉取模型列表失败（{exc}），上游使用默认配置。")


def cmd_run(args: argparse.Namespace) -> int:
    print_banner(args.addr)
    ensure_ready(args.force_login)
    print("\n网关已启动，按 Ctrl+C 停止。")
    gateway_run(args.addr, args.verbose)
    return 0


def cmd_tray(args: argparse.Namespace) -> int:
    # 登录/启动流程在界面里完成（无控制台时也能看到授权链接与重试入口）
    tray_run(args.addr, args.verbose, force_login=args.force_login)
    return 0


def cmd_serve(args: argparse.Namespace) -> int:
    print_banner(args.addr)
    print("\n按 Ctrl+C 停止网关")
    gateway_run(args.addr, args.verbose)
    return 0


def cmd_login(args: argparse.Namespace) -> int:
    if not args.force:
        try:
            load()
            print("已登录，使用 --force 重新登录。")
            return 0
        except NotLoggedInError:
            pass
    c = login(DEFAULT_BASE_URL)
    print(f"登录成功，UID: {c.uid}，域名: {c.domain}")
    return 0


def cmd_doctor(args: argparse.Namespace) -> int:
    base = display_base(args.addr)
    print("WorkBuddyAI Gateway 自检")
    print("========================")
    print(f"网关地址: http://{base}")
    print("端点: /v1/chat/completions\n")

    ok = True
    try:
        c = load()
        print("[ OK ] 凭证文件可读且包含 access token")
    except Exception as exc:
        ok = False
        print(f"[FAIL] 凭证: {exc}")

    if ok:
        cfg = _config_for(c)
        try:
            fetch_models(cfg, c.access_token, c.uid)
            print("[ OK ] 登录状态: token 有效")
        except UpstreamUnauthorized:
            ok = False
            print("[FAIL] 登录状态: token 被上游拒绝，请运行 wbai 重新登录")
        except Exception as exc:
            print(f"[WARN] 登录状态: 验证请求失败（{exc}）")

    if not ok:
        print("\n自检未通过，请按上方提示处理")
        return 1
    print("\n检查通过。")
    return 0


def _log_crash(exc: BaseException) -> None:
    """把异常与堆栈写入 ~/.workbuddyai-gateway/gateway.log。

    --noconsole 打包（双击 exe）时 stderr 不可见，日志文件是唯一的排查线索。
    """
    import time as _time
    import traceback

    with contextlib.suppress(Exception):
        import paths

        paths.ensure_dir()
        stamp = _time.strftime("%Y-%m-%d %H:%M:%S")
        with (paths.base_dir() / "gateway.log").open("a", encoding="utf-8") as f:
            f.write(f"\n[{stamp}] {type(exc).__name__}: {exc}\n")
            f.write("".join(traceback.format_exception(exc)))


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="wbai", description="WorkBuddyAI OpenAI Chat Completion 网关"
    )
    parser.add_argument("--version", action="version", version=f"wbai {__version__}")
    sub = parser.add_subparsers(dest="command")

    p_run = sub.add_parser("run", help="一体式启动：自动检查/触发登录，然后启动网关")
    p_run.add_argument("--addr", default=DEFAULT_ADDR, help="监听地址")
    p_run.add_argument("--verbose", action="store_true", help="打印每个请求")
    p_run.add_argument("--force-login", action="store_true", help="忽略已有凭证，强制重新登录")
    p_run.set_defaults(func=cmd_run)

    p_tray = sub.add_parser("tray", help="托盘模式：右下角图标常驻，状态页自动打开")
    p_tray.add_argument("--addr", default=DEFAULT_ADDR, help="监听地址")
    p_tray.add_argument("--verbose", action="store_true", help="打印每个请求")
    p_tray.add_argument("--force-login", action="store_true", help="忽略已有凭证，强制重新登录")
    p_tray.set_defaults(func=cmd_tray)

    p_login = sub.add_parser("login", help="仅浏览器登录 WorkBuddyAI，保存凭证")
    p_login.add_argument("--force", action="store_true", help="强制重新登录")
    p_login.set_defaults(func=cmd_login)

    p_serve = sub.add_parser("serve", help="跳过登录检查，直接启动网关")
    p_serve.add_argument("--addr", default=DEFAULT_ADDR, help="监听地址")
    p_serve.add_argument("--verbose", action="store_true", help="打印每个请求")
    p_serve.set_defaults(func=cmd_serve)

    p_doctor = sub.add_parser("doctor", help="自检凭证、上游配置与登录状态")
    p_doctor.add_argument("--addr", default=DEFAULT_ADDR, help="网关地址")
    p_doctor.set_defaults(func=cmd_doctor)

    p_ver = sub.add_parser("version", help="打印版本")
    p_ver.set_defaults(func=lambda _args: print(f"wbai {__version__}") or 0)

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command is None:
        # 无参数 = 托盘模式（双击 exe 的默认体验）
        args = parser.parse_args(["tray"])
    try:
        return args.func(args)
    except KeyboardInterrupt:
        print("\n已退出。")
        return 0
    except Exception as exc:
        print(f"\n[ERROR] {exc}", file=sys.stderr)
        _log_crash(exc)
        if sys.stdin.isatty():
            with contextlib.suppress(EOFError, OSError):
                input("Press Enter to exit: ")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
