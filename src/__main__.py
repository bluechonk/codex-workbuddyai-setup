# PyInstaller 入口 / `python src/__main__.py` 直接运行。
# PyInstaller 冻结环境下，导入期崩溃会导致控制台一闪而过，
# 这里在导入 cli 前兜底：把任何异常写进 exe 同目录的 crash.log。
import sys
from pathlib import Path


def _crash_log(exc: BaseException) -> None:
    import contextlib
    import traceback

    if getattr(sys, "frozen", False):
        exe_dir = Path(sys.executable).parent
        with contextlib.suppress(OSError):
            (exe_dir / "crash.log").write_text(traceback.format_exc(), encoding="utf-8")


try:
    from cli import main

    if __name__ == "__main__":
        raise SystemExit(main())
except Exception as exc:  # noqa: BLE001 - 冻结环境最后的兜底
    _crash_log(exc)
    raise
