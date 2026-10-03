# -*- mode: python ; coding: utf-8 -*-
# 产物命名：项目名-版本号（版本号以 src/cli.py 的 __version__ 为唯一来源）。
import re
from pathlib import Path

VERSION = re.search(
    r'__version__ = "([^"]+)"', Path("src/cli.py").read_text(encoding="utf-8")
).group(1)
NAME = f"workbuddyai-gateway-{VERSION}"

a = Analysis(
    ['src/__main__.py'],
    pathex=[],
    binaries=[],
    datas=[('models.json', '.')],
    hiddenimports=[],
    hookspath=[],
    hooksconfig={},
    runtime_hooks=[],
    excludes=[],
    noarchive=False,
    optimize=0,
)
pyz = PYZ(a.pure)

exe = EXE(
    pyz,
    a.scripts,
    a.binaries,
    a.datas,
    [],
    name=NAME,
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=False,  # UPX 显著提高杀软误报率，对字节码为主的包体积收益有限，得不偿失
    upx_exclude=[],
    runtime_tmpdir=None,
    console=False,
    disable_windowed_traceback=False,
    argv_emulation=False,
    target_arch=None,
    codesign_identity=None,
    entitlements_file=None,
)
