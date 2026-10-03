# 入口：双击 exe 即一体式启动（托盘 + 状态窗口），登录流程由窗口状态机承载。
import argparse
import contextlib
import sys

__version__ = "0.1.0"

from tray import run_gui as tray_run

DEFAULT_ADDR = "127.0.0.1:8787"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="workbuddyai-gateway",
        description="WorkBuddyAI OpenAI Chat Completion 网关（一体式托盘启动）",
    )
    parser.add_argument("--version", action="version", version=f"workbuddyai-gateway {__version__}")
    parser.add_argument("--addr", default=DEFAULT_ADDR, help="监听地址")
    parser.add_argument("--verbose", action="store_true", help="打印每个请求")
    parser.add_argument("--force-login", action="store_true", help="忽略已有凭证，强制重新登录")
    args = parser.parse_args(argv)
    try:
        tray_run(args.addr, args.verbose, force_login=args.force_login)
        return 0
    except KeyboardInterrupt:
        return 0
    except Exception as exc:
        print(f"\n[ERROR] {exc}", file=sys.stderr)
        if sys.stdin.isatty():
            with contextlib.suppress(EOFError, OSError):
                input("Press Enter to exit: ")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
