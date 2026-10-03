# 托盘 + Tkinter 主窗口（前端）：右下角图标常驻，窗口深/浅色主题可切换。
# 窗口无边框：无左上角图标与系统菜单；最小化/关闭按钮自绘在窗口右上角，
# 两者都隐藏到托盘（真正退出走托盘菜单）。
from __future__ import annotations

import contextlib
import json
import queue
import sys
import threading
import time
from collections.abc import Callable

import requests

import paths
from portfree import display_base

# ---------------- 主题 ----------------
DARK = {
    "bg": "#111418",
    "card": "#1a1f26",
    "card_border": "#2a323c",
    "text": "#e6e6e6",
    "sub": "#8b93a1",
    "code": "#7dd3fc",
    "model": "#a5d6ff",
    "accent": "#2f6feb",
    "accent_hover": "#3d7bff",
    "accent_text": "#ffffff",
    "ok": "#4ade80",
    "err": "#f87171",
    "btn_bg": "#232a33",
    "hover": "#2f3a46",
    "warn": "#fbbf24",
}
LIGHT = {
    "bg": "#f6f7f9",
    "card": "#ffffff",
    "card_border": "#e3e6ea",
    "text": "#1f2328",
    "sub": "#6b7280",
    "code": "#0969da",
    "model": "#0b62c4",
    "accent": "#2f6feb",
    "accent_hover": "#1f5fd6",
    "accent_text": "#ffffff",
    "ok": "#15803d",
    "err": "#b91c1c",
    "btn_bg": "#eef1f5",
    "hover": "#e2e6ec",
    "warn": "#b45309",
}

THEME_MODES = [("跟随系统", "system"), ("深色模式", "dark"), ("浅色模式", "light")]


def system_theme() -> str:
    """读取 Windows 应用主题（AppsUseLightTheme：1=浅色，0=深色）；其他平台默认深色。"""
    if sys.platform != "win32":
        return "dark"
    try:
        import winreg

        key = winreg.OpenKey(
            winreg.HKEY_CURRENT_USER,
            r"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize",
        )
        with key:
            value, _ = winreg.QueryValueEx(key, "AppsUseLightTheme")
        return "light" if value else "dark"
    except OSError:
        return "dark"


def _prefs_path():

    return paths.prefs_path()


def load_theme_mode() -> str:
    """读取持久化的主题模式，默认跟随系统。"""
    try:
        data = json.loads(_prefs_path().read_text(encoding="utf-8"))
        mode = data.get("theme_mode")
        if mode in ("system", "dark", "light"):
            return mode
    except (OSError, ValueError):
        pass
    return "system"


def save_theme_mode(mode: str) -> None:

    with contextlib.suppress(OSError):
        paths.ensure_dir()
        _prefs_path().write_text(
            json.dumps({"theme_mode": mode}, ensure_ascii=False), encoding="utf-8"
        )


def _icon_image(theme: str = "dark"):
    """用 PIL 现画 64x64 托盘图标：圆角底 + "W"，深浅两套配色。"""
    from PIL import Image, ImageDraw

    bg = (23, 31, 38, 255) if theme == "dark" else (255, 255, 255, 255)
    fg = (125, 211, 252, 255) if theme == "dark" else (47, 111, 235, 255)
    img = Image.new("RGBA", (64, 64), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    d.rounded_rectangle((2, 2, 62, 62), radius=14, fill=bg)
    d.rounded_rectangle((2, 2, 62, 62), radius=14, outline=(48, 111, 235, 255), width=3)
    d.text((20, 16), "W", fill=fg, font_size=32)
    return img


def _apply_menu_theme(theme: str) -> None:
    """让原生弹出菜单（含托盘右键菜单）跟随应用主题。

    Windows 的菜单配色取自进程的 PreferredAppMode（uxtheme.dll 未公开序号 135）：
    ForceDark=2 / ForceLight=3。运行时可重复调用，下一次弹出菜单即生效。
    """
    if sys.platform != "win32":
        return
    import ctypes

    with contextlib.suppress(Exception):
        kernel32 = ctypes.windll.kernel32
        kernel32.LoadLibraryW.restype = ctypes.c_void_p
        kernel32.GetProcAddress.argtypes = [ctypes.c_void_p, ctypes.c_void_p]
        kernel32.GetProcAddress.restype = ctypes.c_void_p
        uxtheme = kernel32.LoadLibraryW("uxtheme.dll")
        addr = kernel32.GetProcAddress(uxtheme, 135)
        if addr:
            set_mode = ctypes.WINFUNCTYPE(ctypes.c_int, ctypes.c_int)(addr)
            set_mode(2 if theme == "dark" else 3)
        # 清菜单主题缓存，避免旧配色残留（序号 136）
        addr_flush = kernel32.GetProcAddress(uxtheme, 136)
        if addr_flush:
            ctypes.WINFUNCTYPE(None)(addr_flush)()


def _enable_dpi_awareness() -> None:
    """声明 Per-Monitor DPI 感知，否则 Windows 位图拉伸窗口导致文字发糊。"""
    if sys.platform != "win32":
        return
    import ctypes

    with contextlib.suppress(Exception):
        ctypes.windll.shcore.SetProcessDpiAwareness(2)
    with contextlib.suppress(Exception):
        ctypes.windll.user32.SetProcessDPIAware()


class GatewayWindow:
    """主窗口：状态 + 可复制地址 + 模型列表 + 主题切换（无边框，自绘窗口按钮）。"""

    WIN_W = 560
    PAD = 20

    def __init__(self, addr: str, verbose: bool) -> None:
        import tkinter as tk

        self.tk = tk
        self.base = display_base(addr)
        self.base_url = f"http://{self.base}/v1"
        self.chat_url = f"http://{self.base}/v1/chat/completions"

        self.mode = load_theme_mode()
        self.theme = "dark"
        self.p = DARK
        self._status_ok: bool | None = None
        self._status_ticks = 0
        self._models_loaded = False
        self._models_shown: list[str] = []
        # 网络探测在后台线程执行，结果经队列回到主线程（Tk 非线程安全，且阻塞请求会卡界面）
        self._probe_q: queue.Queue[dict] = queue.Queue()
        self._probing = False
        # 登录/启动阶段的状态由工作线程经队列投递（Tk 非线程安全）
        self._state_q: queue.Queue[dict] = queue.Queue()
        self._ui_state = "probing"  # probing / login / error / running
        self._login_since: float | None = None  # 登录等待起点（单调时钟）
        self._notice_visible = False  # 提示卡是否显示（由状态机控制）
        self._models_visible = True  # 模型区是否显示（登录/失败时隐藏）
        self._auth_url = ""
        self.retry_evt = threading.Event()
        self.show_evt = threading.Event()
        self.quit_evt = threading.Event()
        self.on_theme_change: Callable[[], None] = lambda: None

        self.root = tk.Tk()
        # 原生窗口栏：拖动/最小化/关闭由系统提供；标题栏配色由 DWM 跟随应用主题
        # （沉浸式深色模式 + CAPTION_COLOR/TEXT_COLOR，已实测深浅色均生效）。
        self.root.title("")
        self.root.resizable(False, False)
        self.root.configure(bg=self.p["bg"])
        # 标题栏图标：自绘 W，随主题更换（清空图标会回落到 Tk 类图标的黑块）
        self._win_icon = None
        self._apply_window_icon()
        # 关闭按钮 = 隐藏到托盘（真正退出走托盘菜单）
        self.root.protocol("WM_DELETE_WINDOW", self.hide_to_tray)
        # 状态文字用持久变量承载：主题重建界面时不会退回"检测中…"（须在 root 之后创建）
        self.status_var = tk.StringVar(value="检测中…")

        self._toast_job: str | None = None
        self._placed = False
        self._first_fit_done = False
        self._build_shell()
        self._apply_theme()

    # ---------- 骨架 ----------
    def _build_shell(self) -> None:
        tk = self.tk
        self.shell = tk.Frame(self.root, bg=self.p["bg"])
        self.shell.pack(fill="both", expand=True)

    # ---------- 主题 ----------
    def resolved_theme(self) -> str:
        return system_theme() if self.mode == "system" else self.mode

    def set_mode(self, mode: str) -> None:
        if mode == self.mode:
            return  # 重复点击当前选项：不触发任何重绘，避免整页闪烁跳动
        self.mode = mode
        save_theme_mode(mode)
        if self.resolved_theme() == self.theme:
            # 选择变了但实际深浅色没变（如 跟随系统(深) ↔ 深色模式）：
            # 只需重绘主题行的高亮位置，整页无需重建
            self._build_theme_row()
        else:
            self._apply_theme()

    def _apply_theme(self) -> None:
        self.theme = self.resolved_theme()
        self.p = DARK if self.theme == "dark" else LIGHT
        self.root.configure(bg=self.p["bg"])
        for w in self.shell.winfo_children():
            w.destroy()
        self._build()
        self._fit_window()
        self._apply_titlebar_theme()
        self._apply_window_icon()
        self.on_theme_change()

    def _apply_window_icon(self) -> None:
        """标题栏图标随主题更换：深色底浅蓝 W / 浅色底蓝 W。"""
        with contextlib.suppress(Exception):
            from PIL import ImageTk

            self._win_icon = ImageTk.PhotoImage(_icon_image(self.theme).resize((32, 32)))
            self.root.iconphoto(True, self._win_icon)

    # ---------- 原生标题栏配色（跟随应用主题） ----------
    @staticmethod
    def _colorref(hex_color: str) -> int:
        """#RRGGBB → Windows COLORREF（0x00BBGGRR）。"""
        r, g, b = (int(hex_color[i : i + 2], 16) for i in (1, 3, 5))
        return r | (g << 8) | (b << 16)

    def _apply_titlebar_theme(self) -> None:
        """让原生标题栏跟随主题：沉浸式深色模式 + 底色/文字色与内容页一致。

        必须在窗口映射后调用（未映射时 HWND 不接受该设置），故带重试。
        """
        if sys.platform != "win32":
            return
        import ctypes

        with contextlib.suppress(Exception):
            if not self.root.winfo_viewable():
                self.root.after(80, self._apply_titlebar_theme)
                return
            u = ctypes.windll.user32
            hwnd = u.GetParent(self.root.winfo_id())
            dark = ctypes.c_int(1 if self.theme == "dark" else 0)
            for attr in (20, 19):  # DWMWA_USE_IMMERSIVE_DARK_MODE（旧系统为 19）
                ctypes.windll.dwmapi.DwmSetWindowAttribute(
                    hwnd, attr, ctypes.byref(dark), ctypes.sizeof(dark)
                )
            # DWMWA_CAPTION_COLOR = 35 / DWMWA_TEXT_COLOR = 36
            for attr, color in ((35, self.p["bg"]), (36, self.p["text"])):
                value = ctypes.c_int(self._colorref(color))
                ctypes.windll.dwmapi.DwmSetWindowAttribute(
                    hwnd, attr, ctypes.byref(value), ctypes.sizeof(value)
                )

    def _fit_window(self) -> None:
        """按内容实际高度自适应窗口；首次出现时在屏幕居中。"""
        self.root.update_idletasks()
        h = self.shell.winfo_reqheight()
        if not self._placed:
            x = max(0, (self.root.winfo_screenwidth() - self.WIN_W) // 2)
            y = max(0, (self.root.winfo_screenheight() - h) // 3)
            self._placed = True
        else:
            x, y = self.root.winfo_x(), self.root.winfo_y()
        self.root.geometry(f"{self.WIN_W}x{h}+{x}+{y}")

    # ---------- 界面 ----------
    def _build(self) -> None:
        tk, p = self.tk, self.p
        frm = tk.Frame(self.shell, bg=p["bg"], padx=24, pady=6)
        frm.pack(fill="both", expand=True)
        self.frm = frm

        self.status_label = tk.Label(
            frm,
            textvariable=self.status_var,
            font=("Microsoft YaHei", 11, "bold"),
            fg=p["ok"] if self._status_ok is None or self._status_ok else p["err"],
            bg=p["bg"],
        )
        self.status_label.pack(anchor="w", pady=(0, 10))

        self._rows_box = tk.Frame(frm, bg=p["bg"])
        self._row(self._rows_box, "Base URL（客户端填这个）", self.base_url)
        self._row(self._rows_box, "Chat Completions 端点", self.chat_url)

        self._build_notice(frm)

        # 模型区与主题区各装一个容器：显隐时整块收放，顺序由 _relayout_sections 固定
        # 主题选择固定在内容最上方（不随登录/运行状态上下浮动）
        self._theme_box = tk.Frame(frm, bg=p["bg"])
        self._build_theme_row()

        self._models_box = tk.Frame(frm, bg=p["bg"])
        self._models_label = tk.Label(
            self._models_box,
            text="可用模型（点击复制）",
            font=("Microsoft YaHei", 9),
            fg=p["sub"],
            bg=p["bg"],
        )
        self._models_label.pack(anchor="w", pady=(0, 2))
        self.btns = tk.Frame(self._models_box, bg=p["bg"])
        self.btns.pack(anchor="w", fill="x")
        # 重建界面（如切换主题）时沿用已拿到的列表，避免闪回"加载中…"；
        # 只有从未拿到过才显示加载态。注意：探测结果带"列表未变则跳过"的去重，
        # 若这里清空成占位符，就再也不会被重新渲染（曾出现的卡住 bug）。
        self._render_models(list(self._models_shown))

        self._relayout_sections()  # 统一决定三个区块的顺序

    def _build_theme_row(self) -> None:
        """构建/重建主题选择行（仅 self._theme_box 内部内容）。

        独立成方法以便局部重建：切换选择但深浅色未变时只重绘这一行，
        避免整页销毁重建造成的闪烁。
        """
        tk, p = self.tk, self.p
        for w in self._theme_box.winfo_children():
            w.destroy()
        tk.Label(
            self._theme_box, text="主题", font=("Microsoft YaHei", 9), fg=p["sub"], bg=p["bg"]
        ).pack(anchor="w", pady=(0, 4))
        seg = tk.Frame(self._theme_box, bg=p["bg"])
        seg.pack(anchor="w")
        for label, mode in THEME_MODES:
            active = mode == self.mode
            btn = self._button(
                seg,
                label,
                lambda m=mode: self.set_mode(m),
                bg=p["accent"] if active else p["btn_bg"],
                fg=p["accent_text"] if active else p["text"],
                padx=2,
                pady=5,
                hover=not active,  # 选中项钝化：悬停不变色
                cursor="arrow" if active else "hand2",  # 选中项不显示手型光标
            )
            btn.configure(width=9)
            btn.pack(side="left", padx=(0, 6))

    def _build_notice(self, parent) -> None:
        """登录/错误提示卡片（默认隐藏，需要时 pack）。"""
        tk, p = self.tk, self.p
        card = tk.Frame(
            parent,
            bg=p["card"],
            padx=12,
            pady=10,
            highlightthickness=1,
            highlightbackground=p["card_border"],
        )
        tk.Label(card, text="提示", font=("Microsoft YaHei", 9), fg=p["sub"], bg=p["card"]).pack(
            anchor="w"
        )
        self._notice_title = tk.Label(
            card, text="", font=("Microsoft YaHei", 10, "bold"), fg=p["text"], bg=p["card"]
        )
        self._notice_title.pack(anchor="w", pady=(2, 4))
        self._notice_msg = tk.Label(
            card,
            text="",
            font=("Microsoft YaHei", 9),
            fg=p["sub"],
            bg=p["card"],
            justify="left",
            wraplength=470,
        )
        self._notice_msg.pack(anchor="w")
        # 等待提示（含已等待秒数）与不确定进度条：让"正在等待授权"有明确反馈
        self._notice_hint = tk.Label(
            card,
            text="",
            font=("Microsoft YaHei", 9),
            fg=p["warn"],
            bg=p["card"],
            justify="left",
            wraplength=470,
        )
        from tkinter import ttk

        # 进度条配色跟随主题（clam 主题才允许配置颜色）
        style = ttk.Style()
        with contextlib.suppress(Exception):
            style.theme_use("clam")
        style.configure(
            "Wbai.Horizontal.TProgressbar",
            troughcolor=p["card"],
            background=p["accent"],
            bordercolor=p["card_border"],
            lightcolor=p["accent"],
            darkcolor=p["accent"],
        )
        self._login_bar = ttk.Progressbar(
            card, mode="indeterminate", length=470, style="Wbai.Horizontal.TProgressbar"
        )
        self._notice_url = tk.Label(
            card,
            text="",
            font=("Consolas", 9),
            fg=p["code"],
            bg=p["card"],
            justify="left",
            wraplength=470,
        )
        row = tk.Frame(card, bg=p["card"])
        row.pack(anchor="w", pady=(8, 0))
        self._notice_row = row

        def _btn(text: str, cmd) -> tk.Button:
            return self._button(row, text, cmd, padx=10)

        import webbrowser as _wb

        self._btn_open = _btn("打开浏览器", lambda: _wb.open(self._auth_url))
        self._btn_copy_url = _btn("复制链接", lambda: self.copy(self._auth_url))
        self._btn_retry = _btn("重试登录", self.retry_evt.set)
        self._notice = card

    def _button(
        self,
        parent,
        text: str,
        command,
        *,
        bg: str | None = None,
        fg: str | None = None,
        font: tuple | None = None,
        padx: int = 12,
        pady: int = 5,
        hover: bool = True,
        cursor: str = "hand2",
    ):
        """统一按钮外观：纯平填充（无系统 3D 边框/高亮环）+ 悬停变色。

        Tk 默认的 borderwidth 与 highlightthickness 用系统色绘制，浅色主题下
        几乎看不出，深色主题下却是一圈发灰的描边——所以这里一律显式置零。
        hover=False 时无悬停反馈（用于当前选中的主题项等"点了也没反应"的按钮）。
        """
        tk, p = self.tk, self.p
        bg = bg or p["accent"]
        fg = fg or p["accent_text"]
        btn = tk.Button(
            parent,
            text=text,
            font=font or ("Microsoft YaHei", 9),
            command=command,
            bg=bg,
            fg=fg,
            activebackground=bg,
            activeforeground=fg,
            relief="flat",
            bd=0,
            highlightthickness=0,
            padx=padx,
            pady=pady,
            cursor=cursor,
        )
        if hover:
            hover_bg = p["accent_hover"] if bg == p["accent"] else p["hover"]
            btn.bind("<Enter>", lambda _e, b=btn, c=hover_bg: b.configure(bg=c))
            btn.bind("<Leave>", lambda _e, b=btn, c=bg: b.configure(bg=c))
        return btn

    def _row(self, parent, label: str, value: str) -> None:
        tk, p = self.tk, self.p
        r = tk.Frame(
            parent,
            bg=p["card"],
            padx=10,
            pady=8,
            highlightthickness=1,
            highlightbackground=p["card_border"],
        )
        r.pack(fill="x", pady=4)
        tk.Label(r, text=label, font=("Microsoft YaHei", 9), fg=p["sub"], bg=p["card"]).pack(
            anchor="w"
        )
        v = tk.Frame(r, bg=p["card"])
        v.pack(fill="x")
        tk.Label(v, text=value, font=("Consolas", 10), fg=p["code"], bg=p["card"]).pack(side="left")
        self._button(v, "复制", lambda: self.copy(value), padx=10).pack(side="right")

    # ---------- 交互 ----------
    def hide_to_tray(self) -> None:
        """最小化/关闭统一行为：隐藏到托盘，网关继续运行。"""
        self.root.withdraw()

    def copy(self, text: str, source: object | None = None) -> None:
        """复制并给出轻量反馈：来源按钮闪一下 + 右下角提示短暂淡出。"""
        self.root.clipboard_clear()
        self.root.clipboard_append(text)
        if source is not None:
            self._flash(source)
        self._toast("已复制")

    def _flash(self, widget) -> None:
        """按钮闪一下示意已复制，随后恢复它原本的配色。"""
        with contextlib.suppress(Exception):
            original = (widget.cget("bg"), widget.cget("fg"))
            widget.configure(bg=self.p["accent"], fg=self.p["accent_text"])
            self.root.after(
                400,
                lambda: widget.winfo_exists() and widget.configure(bg=original[0], fg=original[1]),
            )

    def _toast(self, text: str) -> None:
        tk, p = self.tk, self.p
        if self._toast_job is not None:
            with contextlib.suppress(Exception):
                self.root.after_cancel(self._toast_job)
            self._toast_job = None
        for w in getattr(self, "_toast_widgets", []):
            with contextlib.suppress(Exception):
                w.destroy()
        toast = tk.Label(
            self.root,
            text=f" {text} ",
            font=("Microsoft YaHei", 9),
            fg=p["accent_text"],
            bg=p["accent"],
            padx=6,
            pady=3,
        )
        toast.place(relx=1.0, rely=1.0, x=-16, y=-14, anchor="se")
        self._toast_widgets = [toast]
        self._toast_job = self.root.after(1100, self._clear_toast)

    def _clear_toast(self) -> None:
        for w in getattr(self, "_toast_widgets", []):
            with contextlib.suppress(Exception):
                w.destroy()
        self._toast_widgets = []
        self._toast_job = None

    # ---------- 状态机（工作线程 → 主线程） ----------
    def set_state(self, state: str, message: str = "", url: str = "") -> None:
        """由后台工作线程调用：投递界面状态（登录中 / 失败 / 运行中）。"""
        self._state_q.put({"state": state, "message": message, "url": url})

    def _apply_state(self, payload: dict) -> None:
        """主线程应用状态：登录面板、错误面板、运行中的显隐切换。"""
        state = payload.get("state", "")
        message = payload.get("message", "")
        if payload.get("url"):
            self._auth_url = payload["url"]
        self._ui_state = state
        if state == "login":
            self.status_var.set("● 需要登录")
            self.status_label.configure(fg=self.p["warn"])
            if self._login_since is None:
                self._login_since = time.monotonic()  # 等待起点（用于显示已等待时长）
                self._login_bar.start(12)
            self._show_notice("需要登录 WorkBuddyAI", message, show_url=True, show_retry=True)
            self._set_models_visible(False)
            if self._login_since is None:
                self._login_since = time.monotonic()  # 记录等待起点，用于显示已等待时长
                self._login_bar.pack(anchor="w", fill="x", pady=(8, 0), before=self._notice_url)
                self._login_bar.start(12)
        elif state == "error":
            self.status_var.set("● 启动失败")
            self.status_label.configure(fg=self.p["err"])
            self._show_notice("启动失败", message, show_url=False, show_retry=True)
            self._set_models_visible(False)
        elif state == "running":
            self._stop_login_bar()
            self._show_notice_off()
            self._set_models_visible(True)
            self.start_probe()  # 进入运行态才开始探测
        if state == "error":
            self._stop_login_bar()
        self._fit_window()

    def _relayout_sections(self) -> None:
        """按固定顺序重排区块：主题（最上）→ 地址卡片 → 提示卡 → 可用模型。

        pack_forget 后重新 pack 会跑到末尾，所以显隐必须在这里统一重排，
        否则"重新显示模型区"会把它排到主题行后面（曾出现过该回归）。
        """
        for box in (self._theme_box, self._rows_box, self._notice, self._models_box):
            box.pack_forget()
        # 固定顺序：主题（最上）→ 地址卡片 → 提示卡 → 可用模型
        self._theme_box.pack(anchor="w", pady=(0, 12))
        self._rows_box.pack(fill="x")
        if self._notice_visible:
            self._notice.pack(fill="x", pady=(10, 8))
        if self._models_visible:
            self._models_box.pack(anchor="w", fill="x", pady=(10, 20))  # 底部留白，避免贴着窗口边

    def _stop_login_bar(self) -> None:
        """离开登录态：停止等待进度条并清掉计时（显隐由排版函数负责）。"""
        if self._login_since is None:
            return
        self._login_since = None
        with contextlib.suppress(Exception):
            self._login_bar.stop()

    def _show_notice(self, title: str, message: str, show_url: bool, show_retry: bool) -> None:
        """显示登录/错误提示卡片（URL、打开浏览器、复制链接、重试）。"""
        self._notice_title.configure(text=title)
        self._notice_msg.configure(text=message)
        self._notice_visible = True
        self._relayout_sections()
        has_url = show_url and bool(self._auth_url)
        waiting = self._login_since is not None
        # 固定顺序重排：先全部取下再依次 pack，显隐与顺序都不依赖上一次布局
        for widget in (
            self._notice_title,
            self._notice_msg,
            self._notice_hint,
            self._login_bar,
            self._notice_url,
            self._notice_row,
            self._btn_open,
            self._btn_copy_url,
            self._btn_retry,
        ):
            widget.pack_forget()
        self._notice_title.pack(anchor="w", pady=(2, 4))
        self._notice_msg.pack(anchor="w")
        if has_url:
            self._notice_hint.configure(
                text="浏览器未自动打开？点「打开浏览器」或复制链接手动访问："
            )
            self._notice_hint.pack(anchor="w", pady=(8, 0))
        if waiting:
            self._login_bar.pack(anchor="w", fill="x", pady=(8, 0))
        if has_url:
            self._notice_url.configure(text=self._auth_url)
            self._notice_url.pack(anchor="w", fill="x", pady=(2, 0))
        if has_url or show_retry:
            self._notice_row.pack(anchor="w", pady=(8, 0))
        if has_url:
            self._btn_open.pack(side="left", padx=(0, 6))
            self._btn_copy_url.pack(side="left", padx=(0, 6))
        if show_retry:
            self._btn_retry.pack(side="left", padx=(0, 6))

    def _show_notice_off(self) -> None:
        self._notice_visible = False
        self._relayout_sections()

    def _set_models_visible(self, visible: bool) -> None:
        self._models_visible = visible
        self._relayout_sections()

    # ---------- 数据刷新（后台线程探测，主线程只做 UI 更新） ----------
    def start_probe(self) -> None:
        """后台线程探测 /health 与 /v1/models；结果放入队列由 poll 消费。

        网络请求可能阻塞数秒（网关未响应时），绝不能跑在 Tk 主线程上，
        否则窗口在探测期间无法响应拖动与点击。
        """
        if self._probing:
            return
        self._probing = True
        base = self.base

        def work() -> None:
            result: dict = {}
            try:
                result["ok"] = bool(
                    requests.get(f"http://{base}/health", timeout=3).json().get("ok")
                )
                result["reached"] = True
            except Exception:
                result["ok"] = False
                result["reached"] = False
            try:
                data = requests.get(f"http://{base}/v1/models", timeout=5).json().get("data") or []
                result["ids"] = [m["id"] for m in data if isinstance(m, dict) and m.get("id")]
            except Exception:
                result["ids"] = None  # 保持现状，下次探测再试
            self._probe_q.put(result)

        threading.Thread(target=work, daemon=True).start()

    def apply_probe(self, result: dict) -> None:
        """在主线程应用探测结果。"""
        ok = bool(result.get("ok"))
        reached = bool(result.get("reached"))
        self._status_ok = ok
        if not reached:
            self.status_var.set("● 网关无响应")
        elif ok:
            self.status_var.set("● 运行中 · 凭证有效")
        else:
            self.status_var.set("● 凭证异常，请重新登录")
        with contextlib.suppress(Exception):
            self.status_label.configure(fg=self.p["ok"] if self._status_ok else self.p["err"])
        ids = result.get("ids")
        if ids and ids != self._models_shown:
            self._models_shown = ids
            self._render_models(ids)
            self._fit_window()

    def _render_models(self, ids: list[str]) -> None:
        """按给定 ID 列表重建模型按钮；空列表显示加载态。

        按钮按可用宽度自动换行（模型增多时占多行，而不是撑出窗口）。
        """
        tk, p = self.tk, self.p
        from tkinter import font as tkfont

        for w in self.btns.winfo_children():
            w.destroy()
        if not ids:
            self._models_loaded = False
            tk.Label(
                self.btns,
                text="加载中…",
                font=("Microsoft YaHei", 9),
                fg=p["sub"],
                bg=p["bg"],
            ).pack(anchor="w")
            return
        self._models_loaded = True
        # 可用的按钮区宽度：窗口宽 - 两侧内边距；用实际字体度量估算每个按钮占宽
        avail = self.WIN_W - 2 * 24 - 8
        measure = tkfont.Font(family="Consolas", size=10).measure
        row = tk.Frame(self.btns, bg=p["bg"])
        row.pack(anchor="w", fill="x")
        used = 0
        for mid in ids:
            width = measure(mid) + 28  # 文本 + padx*2 + 描边
            if used and used + width > avail:  # 放不下就换行
                row = tk.Frame(self.btns, bg=p["bg"])
                row.pack(anchor="w", fill="x", pady=(6, 0))
                used = 0
            btn = tk.Button(
                row,
                text=mid,
                font=("Consolas", 10),
                bg=p["btn_bg"],
                fg=p["model"],
                activebackground=p["accent"],
                activeforeground=p["accent_text"],
                relief="flat",
                bd=0,
                highlightthickness=1,
                highlightbackground=p["card_border"],
                padx=10,
                pady=5,
                cursor="hand2",
            )
            btn.configure(command=lambda mid=mid, b=btn: self.copy(mid, b))
            btn.bind("<Enter>", lambda _e, b=btn: b.configure(bg=p["hover"]))
            btn.bind("<Leave>", lambda _e, b=btn: b.configure(bg=p["btn_bg"]))
            btn.pack(side="left", padx=(0, 6))
            used += width + 6

    # ---------- 主循环 ----------
    def _reveal(self) -> None:
        """把窗口显示到最前：置顶闪烁 + Win32 前台置顶（点击托盘后本进程有前台权限）。"""
        self.root.deiconify()
        self.root.lift()
        with contextlib.suppress(Exception):
            self.root.attributes("-topmost", True)
            self.root.after(250, lambda: self.root.attributes("-topmost", False))
        if sys.platform == "win32":
            import ctypes

            with contextlib.suppress(Exception):
                u = ctypes.windll.user32
                hwnd = u.GetParent(self.root.winfo_id())
                u.ShowWindow(hwnd, 9)  # SW_RESTORE
                # 经典强制前置：TOPMOST → NOTOPMOST（无需前台权限也会抬到普通窗口之上）
                SWP_NOMOVE_NOSIZE = 0x0002 | 0x0001
                u.SetWindowPos(hwnd, -1, 0, 0, 0, 0, SWP_NOMOVE_NOSIZE)  # HWND_TOPMOST
                u.SetWindowPos(hwnd, -2, 0, 0, 0, 0, SWP_NOMOVE_NOSIZE)  # HWND_NOTOPMOST
                u.BringWindowToTop(hwnd)
                u.SetForegroundWindow(hwnd)

    def poll(self) -> None:
        if not self._first_fit_done:
            # 首帧时布局才真正稳定，按最终内容重新贴合高度
            self._first_fit_done = True
            self._fit_window()
        if self.quit_evt.is_set():
            self.root.destroy()
            return
        if self.show_evt.is_set():
            self.show_evt.clear()
            self._reveal()
            self.root.after(80, self._apply_titlebar_theme)  # 恢复显示后重新套用标题栏主题
            self.root.after(120, self._apply_window_icon)
        # 登录等待中：每秒刷新已等待时长，让用户知道流程仍在推进
        if self._ui_state == "login" and self._login_since is not None:
            waited = int(time.monotonic() - self._login_since)
            if waited >= 3:
                self._notice_hint.configure(
                    text=f"已等待 {waited} 秒（每轮最长 5 分钟，超时会自动重试）"
                )
        # 消费状态机更新（登录中/失败/运行中）
        while True:
            try:
                payload = self._state_q.get_nowait()
            except queue.Empty:
                break
            self._apply_state(payload)
        # 消费后台探测结果（非阻塞）
        while True:
            try:
                result = self._probe_q.get_nowait()
            except queue.Empty:
                break
            self._probing = False
            self.apply_probe(result)
        # 周期性重探（每秒 tick，5 次一探），探测本身在后台线程，不阻塞界面
        self._status_ticks += 1
        if self._status_ticks >= 5 and self._ui_state == "running":
            self._status_ticks = 0
            self.start_probe()
        # 兜底：菜单主题未同步时按当前主题重设（每秒校验，开销可忽略）
        if getattr(self, "_menu_theme", None) != self.theme:
            _apply_menu_theme(self.theme)
            self._menu_theme = self.theme
        if self.mode == "system" and self.resolved_theme() != self.theme:
            self._apply_theme()
        self.root.after(1000, self.poll)

    def run(self) -> None:
        self.poll()
        self.root.after(60, self._reveal)  # 启动即置于最前
        self.root.after(120, self._apply_titlebar_theme)  # 映射后套用标题栏主题
        self.root.after(160, self._apply_window_icon)  # 映射后重设图标（确保标题栏显示）
        self.root.mainloop()


def _ensure_login_for_gui(win: GatewayWindow) -> bool:
    """保证存在可用登录凭证；必要时在界面里引导登录。

    返回 True 表示可以启动网关；False 表示登录/探测失败（界面已提示）。
    """
    import cred
    import upstream

    def cfg_for(c) -> upstream.Config:
        try:
            cfg, _ok = upstream.load_config()
        except Exception:
            cfg = upstream.default_config()
        if c is not None and getattr(c, "domain", "") and c.domain != _host_of(cfg.base_url):
            cfg.base_url = "https://" + c.domain
        return cfg

    try:
        c = cred.load()
    except cred.NotLoggedInError:
        c = None
    except Exception as exc:
        win.set_state("login", f"凭证读取失败（{exc}），需要重新登录。")
        c = None

    if c is None:
        # 界面进入登录态：展示授权链接 + 打开浏览器/复制/重试
        win.set_state("login", "正在申请授权链接…")
        try:
            c = cred.login(
                cred.DEFAULT_BASE_URL,
                on_url=lambda url: win.set_state(
                    "login", "请在弹出的浏览器里完成授权，本窗口稍后自动继续。", url
                ),
                on_status=lambda msg: win.set_state("login", msg, win._auth_url),
            )
        except Exception as exc:
            win.set_state("error", f"登录失败：{exc}")
            return False
        win.set_state("login", "授权成功，正在初始化…", win._auth_url)
        with contextlib.suppress(Exception):
            data = upstream.fetch_models(cfg_for(c), c.access_token, c.uid)
            upstream.save_config(upstream.resolve_config(data, cfg_for(c)))
        return True

    # 有凭证：探测登录状态（失效先刷新，仍失败则引导重新登录）
    cfg = cfg_for(c)
    try:
        upstream.fetch_models(cfg, c.access_token, c.uid)
        return True
    except upstream.UpstreamUnauthorized:
        try:
            c = cred.refresh(c)
            upstream.fetch_models(cfg_for(c), c.access_token, c.uid)
            return True
        except Exception as exc:
            win.set_state("error", f"凭据已失效且刷新失败（{exc}），请点「重试登录」。")
            return False
    except Exception as exc:
        # 网络不通等临时问题：照常启动，运行期请求失败会自动刷新
        win.set_state("error", f"登录状态验证未通过（{exc}），仍将继续启动。")
        return True


def _host_of(base_url: str) -> str:
    """从 baseUrl 提取 host。"""
    from urllib.parse import urlsplit

    return urlsplit(base_url).netloc or base_url.split("/")[0]


def run_gui(addr: str, verbose: bool, force_login: bool = False) -> None:
    """托盘模式入口：先创建界面与托盘图标，再在后台完成登录与网关启动。

    这样"需要登录"时用户能看到窗口（授权链接、打开浏览器、重试），
    而不是双击后什么也不显示。
    """
    _enable_dpi_awareness()

    import pystray

    import gateway

    if force_login:
        # 强制重登：先清掉本地凭证，让流程走登录分支
        with contextlib.suppress(Exception):
            paths.credentials_path().unlink(missing_ok=True)

    win = GatewayWindow(addr, verbose)

    def _open_window(*_: object) -> None:
        win.show_evt.set()

    def _quit(*_: object) -> None:
        win.quit_evt.set()

    icon = pystray.Icon(
        "wbai",
        _icon_image(win.theme),
        f"WorkBuddyAI Gateway · http://{win.base}",
        pystray.Menu(
            pystray.MenuItem("打开主窗口", _open_window, default=True),
            pystray.MenuItem("退出", _quit),
        ),
    )

    def _sync_theme() -> None:
        icon.icon = _icon_image(win.theme)
        _apply_menu_theme(win.theme)

    win.on_theme_change = _sync_theme
    _apply_menu_theme(win.theme)
    threading.Thread(target=icon.run, daemon=True).start()

    stop_holder: dict[str, Callable[[], None] | None] = {"stop": None}

    def _gateway_failed(exc: Exception) -> None:
        win.set_state("error", f"网关启动失败：{exc}")

    def worker() -> None:
        while True:
            # 1) 登录就绪（必要时在界面里完成登录）
            if _ensure_login_for_gui(win):
                break
            # 2) 用户点"重试"后重新走一遍
            win.retry_evt.wait()
            win.retry_evt.clear()
        # 3) 启动网关
        try:
            stop_holder["stop"] = gateway.start_background(addr, verbose, _gateway_failed)
        except Exception as exc:
            win.set_state("error", f"网关启动失败：{exc}")
            return
        win.set_state("running")

    threading.Thread(target=worker, daemon=True).start()

    win.run()

    icon.stop()
    if stop_holder["stop"]:
        stop_holder["stop"]()
