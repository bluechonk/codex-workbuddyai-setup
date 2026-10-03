# 一键构建 Windows 产物：PyInstaller onedir + Inno Setup 安装器。
# 用法：uv run python build.py [--skip-installer]
# 版本号唯一来源：src/cli.py 的 __version__。
# 产物：dist/workbuddyai-gateway-<版本>/ 与 dist/installer/*.exe
from __future__ import annotations

import argparse
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).parent


def main() -> int:
    parser = argparse.ArgumentParser(description="构建 WorkBuddyAI Gateway Windows 产物")
    parser.add_argument(
        "--skip-installer", action="store_true", help="只跑 PyInstaller，不打安装器"
    )
    args = parser.parse_args()

    version = re.search(
        r'__version__ = "([^"]+)"', (ROOT / "src" / "cli.py").read_text(encoding="utf-8")
    ).group(1)
    print(f"[build] 版本号: {version}")

    # 1) PyInstaller onedir
    subprocess.run(
        [sys.executable, "-m", "PyInstaller", "workbuddyai-gateway.spec", "--noconfirm"],
        cwd=ROOT,
        check=True,
    )
    bundle = ROOT / "dist" / f"workbuddyai-gateway-{version}"
    if not (bundle / f"workbuddyai-gateway-{version}.exe").exists():
        print(f"[build] 失败：未找到 {bundle}")
        return 1
    print(f"[build] PyInstaller 完成: {bundle}")

    # 2) Inno Setup 安装器
    if args.skip_installer:
        print("[build] 按参数跳过安装器")
        return 0
    iscc_candidates = [
        Path.home() / "AppData/Local/Programs/Inno Setup 6/ISCC.exe",
        Path(r"C:\Program Files (x86)\Inno Setup 6\ISCC.exe"),
        Path(r"C:\Program Files\Inno Setup 6\ISCC.exe"),
    ]
    iscc = next((p for p in iscc_candidates if p.exists()), None)
    if iscc is None:
        print("[build] 未找到 ISCC.exe，请安装 Inno Setup 6: winget install JRSoftware.InnoSetup")
        return 1
    subprocess.run(
        [str(iscc), f"/DAppVersion={version}", str(ROOT / "workbuddyai-gateway.iss")],
        cwd=ROOT,
        check=True,
    )
    print(f"[build] 安装器完成: dist/installer/workbuddyai-gateway-{version}-setup.exe")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
