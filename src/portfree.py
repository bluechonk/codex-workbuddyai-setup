# 端口占用处理：监听失败时识别占用进程；仅自动结束自身的残留实例，
# 其他进程需用户确认后才结束，等待端口释放后重试。
# 移植自 Go 版 cmd/wbai/port.go。
from __future__ import annotations

import os
import signal
import socket
import subprocess
import sys
import time
from collections.abc import Callable


class PortBusyError(RuntimeError):
    """端口被占用且无法自动释放。"""


def _dedupe(pids: list[int]) -> list[int]:
    seen: set[int] = set()
    out: list[int] = []
    for pid in pids:
        if pid not in seen:
            seen.add(pid)
            out.append(pid)
    return out


def find_port_pids(port: int) -> list[int]:
    """返回正在监听指定端口的所有进程 PID。"""
    if sys.platform == "win32":
        return _find_port_pids_windows(port)
    return _find_port_pids_unix(port)


def _find_port_pids_windows(port: int) -> list[int]:
    # 解析 `netstat -ano -p tcp` 输出，行形如：
    #   TCP    127.0.0.1:8787    0.0.0.0:0    LISTENING    12345
    suffix = f":{port}"
    try:
        out = subprocess.run(
            ["netstat", "-ano", "-p", "tcp"],
            capture_output=True,
            check=True,
        ).stdout.decode(errors="replace")
    except (OSError, subprocess.CalledProcessError):
        return []
    pids: list[int] = []
    for line in out.split("\n"):
        fields = line.split()
        if len(fields) < 5 or fields[0] != "TCP" or fields[3] != "LISTENING":
            continue
        if not fields[1].endswith(suffix):
            continue
        try:
            pid = int(fields[-1])
        except ValueError:
            continue
        if pid > 0:
            pids.append(pid)
    return _dedupe(pids)


def _find_port_pids_unix(port: int) -> list[int]:
    try:
        out = subprocess.run(
            ["lsof", "-ti", f"tcp:{port}", "-sTCP:LISTEN"],
            capture_output=True,
            check=True,
        ).stdout.decode(errors="replace")
    except (OSError, subprocess.CalledProcessError):
        return []
    pids: list[int] = []
    for line in out.strip().split("\n"):
        line = line.strip()
        if not line:
            continue
        try:
            pid = int(line)
        except ValueError:
            continue
        if pid > 0:
            pids.append(pid)
    return _dedupe(pids)


def exclude_self(pids: list[int]) -> list[int]:
    """过滤掉自身与系统进程 PID（0/4），避免误杀。"""
    self_pid = os.getpid()
    return [p for p in pids if p != self_pid and p != 0 and p != 4]


def proc_name(pid: int) -> str:
    """返回进程名（用于区分自身残留实例与第三方进程），失败返回空。"""
    if sys.platform == "win32":
        try:
            out = subprocess.run(
                ["tasklist", "/FI", f"PID eq {pid}", "/FO", "CSV", "/NH"],
                capture_output=True,
                check=True,
            ).stdout.decode(errors="replace")
        except (OSError, subprocess.CalledProcessError):
            return ""
        line = out.strip()
        # CSV 形如: "workbuddyai-gateway-1.0.0.exe","12345","Console",...
        i = line.find(",")
        if i <= 0:
            return ""
        name = line[:i].strip().strip('"')
        if name.lower().endswith(".exe"):
            name = name[: -len(".exe")]
        return name.lower()
    try:
        out = subprocess.run(
            ["ps", "-p", str(pid), "-o", "comm="],
            capture_output=True,
            check=True,
        ).stdout.decode(errors="replace")
    except (OSError, subprocess.CalledProcessError):
        return ""
    return os.path.basename(out.strip())


def exe_name() -> str:
    """返回当前可执行文件的基础名，统一去掉 .exe 后缀以便与进程名比较。"""
    base = os.path.basename(sys.argv[0])
    if base.lower().endswith(".exe"):
        base = base[: -len(".exe")]
    return base.lower()


def kill_pid(pid: int) -> None:
    """结束指定进程；非 Windows 先尝试 SIGTERM，超时再 SIGKILL。"""
    if sys.platform == "win32":
        subprocess.run(
            ["taskkill", "/F", "/PID", str(pid)],
            capture_output=True,
            check=True,
        )
        return
    os.kill(pid, signal.SIGTERM)
    deadline = time.monotonic() + 3.0
    while time.monotonic() < deadline:
        try:
            os.kill(pid, 0)
        except OSError:
            return  # 进程已退出
        time.sleep(0.1)
    os.kill(pid, signal.SIGKILL)


def _bind_listen(host: str, port: int) -> socket.socket:
    """直接 bind+listen，失败抛 OSError。"""
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        sock.bind((host, port))
        sock.listen(128)
    except OSError:
        sock.close()
        raise
    return sock


def bind_free_socket(host: str, port: int, printf: Callable[[str], None]) -> socket.socket:
    """在 (host, port) 上建立 TCP 监听；若端口被其他进程占用：
    - 占用进程是自身可执行文件的残留实例 → 自动结束并重试；
    - 其他进程 → 打印进程名并请求确认，拒绝或非交互环境下报错。
    成功返回已 listen 的 socket（对应 Go net.Listen 的行为，不设 SO_REUSEADDR）。
    """
    try:
        return _bind_listen(host, port)
    except OSError:
        pass

    pids = exclude_self(find_port_pids(port))
    if not pids:
        raise PortBusyError(f"端口 {port} 被占用，未能识别占用进程，请手动释放后重试")
    self_name = exe_name()
    for pid in pids:
        name = proc_name(pid)
        # 只有确认为自身同名进程才自动结束；进程名未知时按第三方处理（需确认），
        # 否则识别失败会变成"静默杀掉任意进程"。
        if name and name == self_name:
            printf(f"端口 {port} 被自身的残留实例占用（{name}, PID {pid}），正在结束它...")
        else:
            printf(f"端口 {port} 被其他进程占用：{name or '未知进程'} (PID {pid})")
            if not _confirm_kill(port):
                raise PortBusyError(
                    f"端口 {port} 被进程 {name or '未知进程'} (PID {pid}) 占用，已取消；"
                    f"请更换 --addr 或手动释放端口"
                )
        try:
            kill_pid(pid)
        except (OSError, subprocess.CalledProcessError) as exc:
            raise PortBusyError(f"结束占用进程 {pid} 失败: {exc}") from exc

    deadline = time.monotonic() + 5.0
    last_err: OSError | None = None
    while True:
        if time.monotonic() >= deadline:
            raise PortBusyError(
                f"端口 {port} 在结束占用进程后仍未释放，请稍后重试（最后错误: {last_err}）"
            )
        time.sleep(0.3)
        try:
            return _bind_listen(host, port)
        except OSError as exc:
            last_err = exc


def _confirm_kill(port: int) -> bool:
    """请求用户确认是否结束占用端口的第三方进程。"""
    if not sys.stdin.isatty():
        return False
    try:
        ans = input(f"是否结束该进程以释放端口 {port}？[y/N] ").strip().lower()
    except (EOFError, OSError):
        return False
    return ans in ("y", "yes")


def display_base(addr: str) -> str:
    """把监听地址转成可展示的 host:port（空/通配 host 回显为 127.0.0.1）。"""
    # 等价 Go net.SplitHostPort：要求形如 host:port 或 [h]:port；解析失败原样返回
    if addr.count(":") > 1:
        if not addr.startswith("["):
            return addr  # 裸 IPv6（如 ::1），无法按 host:port 拆分
        host, _, rest = addr[1:].partition("]")
        port = rest[1:] if rest.startswith(":") else ""
    else:
        host, sep, port = addr.rpartition(":")
        if not sep or not port:
            return addr
    if host in ("", "0.0.0.0", "::"):
        host = "127.0.0.1"
    return f"{host}:{port}"
