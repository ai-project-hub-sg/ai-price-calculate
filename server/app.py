"""轻量后端：静态文件 + 配置读写 + 汇率 + 价目更新任务。

零依赖（Python 3.10+ 标准库）。启动：
    python -m server.app [--port 8787]
接口：
    GET  /                     → public/index.html
    POST /api/auth/login       → 登录并返回 Bearer token
    POST /api/auth/logout      → 注销当前 token
    GET  /api/models           → data/models.json
    GET  /api/config           → data/config.json（不存在则返回默认）
    PUT  /api/config           → 写回 data/config.json
    GET  /api/fx               → {rate, source, fetchedAt, cached}
    POST /api/pricing/update   → {"jobId": "..."} 后台跑 fetcher.update
    POST /api/nodes/test       → {"jobId": "..."} 后台跑节点连通性探测
    GET  /api/jobs/<id>        → {"status": running|done|error, "report": ...}

除登录、OPTIONS 和静态文件外，/api/ 端点均要求 Authorization: Bearer <token>。
"""
from __future__ import annotations

import argparse
import copy
import json
import os
import signal
import sys
import tempfile
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from . import fetcher, nodepool, security, validation

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PUBLIC = os.path.join(ROOT, "public")
DATA = os.path.join(ROOT, "data")
CONFIG_PATH = os.path.join(DATA, "config.json")
FX_CACHE = os.path.join(DATA, "fx-cache.json")
FX_URL = "https://open.er-api.com/v6/latest/USD"
FX_TTL = 24 * 3600
MAX_BODY_BYTES = 2 * 1024 * 1024

DEFAULT_CONFIG = {
    "version": 1,
    "scenario": {"inputTokens": 20000, "outputTokens": 4000,
                 "cacheReadRatio": 0.3, "requests": 1},
    "tieredPricing": {"enabled": True},
    "currency": {"display": "both", "usdCny": None, "autoFx": True},
    "defaults": {"discount": 1.0},
    "stations": [],
}

_JOBS: dict[str, dict] = {}
_JOBS_LOCK = threading.Lock()
_CONFIG_LOCK = threading.Lock()


def _read_json(path: str, default):
    """宽松读：仅用于可再生的缓存文件（fx-cache）。"""
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except (OSError, json.JSONDecodeError):
        return default


def _read_json_strict(path: str):
    """严格读：用户数据文件损坏时抛错返回 500，不静默降级
    （静默降级会让下一次写回无声覆盖损坏数据）。"""
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def _write_config(cfg: dict):
    """调用方持有 _CONFIG_LOCK；同目录唯一临时文件保证并发写互不覆盖。"""
    os.makedirs(DATA, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=".config-", suffix=".tmp", dir=DATA)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(cfg, f, ensure_ascii=False, indent=2)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, CONFIG_PATH)
    finally:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass


def _fx() -> dict:
    cache = _read_json(FX_CACHE, None)
    if cache and time.time() - cache.get("ts", 0) < FX_TTL:
        return {**cache["data"], "cached": True}
    # 严格直连（nodepool.fetch_url 显式空 ProxyHandler，不读环境变量代理）
    r = nodepool.fetch_url(FX_URL, None, timeout=10)
    if r["status"] != 200:
        raise RuntimeError(f"汇率源 HTTP {r['status']} {r.get('error','')}")
    payload = json.loads(r["body"])
    data = {"rate": payload["rates"]["CNY"], "source": FX_URL,
            "fetchedAt": payload.get("time_last_update_utc", "")}
    os.makedirs(DATA, exist_ok=True)
    with open(FX_CACHE, "w", encoding="utf-8") as f:
        json.dump({"ts": time.time(), "data": data}, f)
    return {**data, "cached": False}


def _run_update(job_id: str, providers: list[str] | None, max_nodes: int):
    lines: list[str] = []

    def prog(p: dict):
        with _JOBS_LOCK:
            _JOBS[job_id]["progress"] = p

    try:
        rep = fetcher.update(providers, max_nodes=max_nodes,
                             log=lambda m: lines.append(str(m)),
                             progress=prog)
        status, err = "done", None
    except Exception as e:
        rep, status, err = None, "error", f"{type(e).__name__}: {e}"
    with _JOBS_LOCK:
        _JOBS[job_id].update({"status": status, "report": rep,
                              "log": lines[-200:], "error": err})


def _run_nodes_test(job_id: str, force: bool, max_nodes: int):
    """POST /api/nodes/test 后台任务：v2rayN 候选节点连通性探测。
    1h 内测过 → 直接回缓存快照（cached 标记），force=True 强制重跑。"""
    lines: list[str] = []
    rep, status, err = None, "done", None
    try:
        cached = None if force else nodepool.cache_test_results()
        if cached:
            res, at = cached
            usable = sum(1 for r in res if r["ok"])
            lines.append(f"缓存命中：{int(time.time() - at)}s 前的探测结果"
                         "（1h 内有效），重跑请点「强制重测」")
            rep = {"total": len(nodepool._load_candidates()),
                   "probed": len(res), "usable": usable,
                   "results": res, "cached": True}
        else:
            nodes = nodepool._load_candidates()
            probed = min(max_nodes, len(nodes)) if max_nodes > 0 else len(nodes)
            lines.append(f"候选节点 {len(nodes)} 个，探测前 {probed}"
                         f"（连通性目标 {nodepool.PROBE_URL}）…")
            res = nodepool.test_nodes(
                nodes, max_nodes=max_nodes,
                on_progress=lambda p: lines.append(
                    f"  已测 {p['tested']}/{p['total']}"))
            usable = sum(1 for r in res if r["ok"])
            lines.append(f"完成：{usable}/{len(res)} 节点可用")
            rep = {"total": len(nodes), "probed": len(res),
                   "usable": usable, "results": res}
    except Exception as e:
        status, err = "error", f"{type(e).__name__}: {e}"
    with _JOBS_LOCK:
        _JOBS[job_id].update({"status": status, "report": rep,
                              "log": lines[-200:], "error": err})


_MIME = {".html": "text/html; charset=utf-8", ".js": "text/javascript",
         ".css": "text/css", ".json": "application/json; charset=utf-8",
         ".svg": "image/svg+xml", ".ico": "image/x-icon"}


class Handler(BaseHTTPRequestHandler):
    def end_headers(self):
        # 开发期禁用缓存，避免浏览器拿到旧版页面/数据
        self.send_header("Cache-Control", "no-store")
        super().end_headers()

    def _send(self, code: int, body: bytes, ctype="application/json; charset=utf-8"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(body)

    def _json(self, obj, code=200):
        self._send(code, json.dumps(obj, ensure_ascii=False).encode("utf-8"))

    def _body(self) -> dict | None:
        """解析 JSON 对象；失败时直接返回 400，调用方不得继续写配置。"""
        if self.headers.get("Transfer-Encoding"):
            self._json({"error": "不支持 Transfer-Encoding，请使用 Content-Length"}, 400)
            return None
        raw_length = self.headers.get("Content-Length")
        try:
            n = int(raw_length) if raw_length is not None else 0
        except ValueError:
            self._json({"error": "Content-Length 无效"}, 400)
            return None
        if n < 0:
            self._json({"error": "Content-Length 无效"}, 400)
            return None
        if n > MAX_BODY_BYTES:
            self._json({"error": "请求体超过 2MB 上限"}, 413)
            return None
        if not n:
            return {}
        raw = self.rfile.read(n)
        if len(raw) != n:
            self._json({"error": "请求体不完整"}, 400)
            return None
        try:
            body = json.loads(raw)
        except (json.JSONDecodeError, UnicodeDecodeError):
            self._json({"error": "请求体必须是合法的 JSON 对象"}, 400)
            return None
        if not isinstance(body, dict):
            self._json({"error": "请求体必须是 JSON 对象"}, 400)
            return None
        return body

    def _authorize(self) -> str | None:
        parts = self.headers.get("Authorization", "").split()
        if len(parts) != 2 or parts[0].lower() != "bearer":
            self._json({"error": "需要有效的 Bearer token"}, 401)
            return None
        token = parts[1]
        if self.server.auth.authenticate(token) is None:
            self._json({"error": "需要有效的 Bearer token"}, 401)
            return None
        return token

    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET,PUT,POST,OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type, Authorization")
        self.end_headers()

    def do_GET(self):
        path = self.path.split("?")[0]
        if path.startswith("/api/") and self._authorize() is None:
            return
        if path == "/api/models":
            try:
                self._json(_read_json_strict(os.path.join(DATA, "models.json")))
            except FileNotFoundError:
                self._json({})
            except Exception as e:
                self._json({"error": f"models.json 解析失败: {e}"}, 500)
        elif path == "/api/config":
            try:
                self._json(_read_json_strict(CONFIG_PATH))
            except FileNotFoundError:
                self._json(DEFAULT_CONFIG)
            except Exception as e:
                self._json({"error": f"config.json 解析失败: {e}"}, 500)
        elif path == "/api/fx":
            try:
                self._json(_fx())
            except Exception as e:
                self._json({"error": f"{type(e).__name__}: {e}"}, 502)
        elif path == "/api/nodes":
            try:
                ns = nodepool._load_candidates()
                self._json({"total": len(ns), "nodes": [
                    {"id": n.index_id, "name": n.name, "kind": n.kind,
                     "delay": n.delay} for n in ns]})
            except Exception as e:
                self._json({"error": f"{type(e).__name__}: {e}"}, 500)
        elif path.startswith("/api/jobs/"):
            with _JOBS_LOCK:
                job = copy.deepcopy(_JOBS.get(path.rsplit("/", 1)[-1]))
            self._json(job or {"error": "not found"}, 200 if job else 404)
        else:
            self._static(path)

    def _static(self, path: str):
        rel = "index.html" if path in ("/", "") else path.lstrip("/")
        fp = os.path.normpath(os.path.join(PUBLIC, rel))
        if not fp.startswith(PUBLIC + os.sep) or not os.path.isfile(fp):
            return self._send(404, b"not found", "text/plain")
        with open(fp, "rb") as f:
            self._send(200, f.read(), _MIME.get(os.path.splitext(fp)[1],
                                              "application/octet-stream"))

    def do_PUT(self):
        path = self.path.split("?")[0]
        if path.startswith("/api/") and self._authorize() is None:
            return
        if path == "/api/config":
            cfg = self._body()
            if cfg is None:
                return
            if "proxySubscription" in cfg and cfg["proxySubscription"] != "":
                try:
                    cfg["proxySubscription"] = validation.validate_proxy_url(
                        cfg["proxySubscription"])
                except ValueError as e:
                    self._json({"error": str(e)}, 400)
                    return
            try:
                with _CONFIG_LOCK:
                    _write_config(cfg)
            except (OSError, TypeError, ValueError) as e:
                self._json({"error": f"配置写入失败: {e}"}, 500)
                return
            self._json({"ok": True})
        else:
            self._send(404, b"not found", "text/plain")

    def _max_nodes(self, body: dict) -> int | None:
        try:
            value = int(body.get("maxNodes", 25))
        except (TypeError, ValueError, OverflowError):
            self._json({"error": "maxNodes 必须是整数"}, 400)
            return None
        if value < 0:
            self._json({"error": "maxNodes 不能为负数"}, 400)
            return None
        return value

    def _start_job(self, job_id: str, job: dict) -> bool:
        # 与 shutdown 共用实例状态锁，消除“检查后立刻又有新 job”的竞态。
        with self.server.state_lock:
            if self.server.shutting_down:
                self._json({"error": "服务正在关闭，无法启动新任务"}, 409)
                return False
            with _JOBS_LOCK:
                _JOBS[job_id] = job
        return True

    def do_POST(self):
        path = self.path.split("?")[0]
        if path == "/api/auth/login":
            body = self._body()
            if body is None:
                return
            try:
                session = self.server.auth.login(
                    body.get("username"), body.get("password"),
                    self.client_address[0])
            except security.RateLimited:
                self._json({"error": "登录尝试过于频繁，请稍后重试"}, 429)
            except security.InvalidCredentials:
                self._json({"error": "用户名或密码错误"}, 401)
            else:
                self._json(session)
            return
        if path.startswith("/api/") and self._authorize() is None:
            return
        if path == "/api/auth/logout":
            # 鉴权成功后重新取原始 token，避免把 Authorization 方案名交给 auth。
            token = self.headers["Authorization"].split(maxsplit=1)[1]
            self.server.auth.logout(token)
            self._json({"ok": True})
        elif path == "/api/pricing/update":
            body = self._body()
            if body is None:
                return
            max_nodes = self._max_nodes(body)
            if max_nodes is None:
                return
            job_id = uuid.uuid4().hex[:8]
            if not self._start_job(job_id, {
                    "status": "running", "report": None, "log": [],
                    "progress": None,
                    "startedAt": int(time.time() * 1000)}):
                return
            threading.Thread(
                target=_run_update,
                args=(job_id, body.get("providers"), max_nodes),
                daemon=True).start()
            self._json({"jobId": job_id})
        elif path == "/api/nodes/test":
            body = self._body()
            if body is None:
                return
            url = body.get("url", "")
            if url is not None and url != "":
                try:
                    validation.validate_proxy_url(url)
                except ValueError as e:
                    self._json({"error": str(e)}, 400)
                    return
            # Python 仍探测本地 v2rayN 候选；url 只校验，不拉取订阅。
            max_nodes = self._max_nodes(body)
            if max_nodes is None:
                return
            job_id = uuid.uuid4().hex[:8]
            if not self._start_job(job_id, {
                    "status": "running", "report": None, "log": [],
                    "progress": None,
                    "startedAt": int(time.time() * 1000)}):
                return
            threading.Thread(
                target=_run_nodes_test,
                args=(job_id, bool(body.get("force")), max_nodes),
                daemon=True).start()
            self._json({"jobId": job_id})
        elif path == "/api/shutdown":
            with self.server.state_lock:
                with _JOBS_LOCK:
                    running = any(j.get("status") == "running"
                                  for j in _JOBS.values())
                if not running:
                    self.server.shutting_down = True
            if running:
                self._json({"error": "仍有运行中的任务，无法关闭服务"}, 409)
                return
            # 先完整响应，再从独立线程停机（不能在 serve_forever 线程内调用）。
            self._json({"ok": True, "shutdown": True})
            self.wfile.flush()
            threading.Thread(target=self.server.shutdown, daemon=True).start()
        else:
            self._send(404, b"not found", "text/plain")

    def _unsupported(self):
        path = self.path.split("?")[0]
        if path.startswith("/api/") and self._authorize() is None:
            return
        self._json({"error": "method not allowed"}, 405)

    do_DELETE = _unsupported
    do_PATCH = _unsupported
    do_HEAD = _unsupported

    def send_error(self, code, message=None, explain=None):
        # BaseHTTPRequestHandler 对未实现方法默认 501；API 也不能绕过鉴权。
        if code == 501 and getattr(self, "path", "").startswith("/api/"):
            if self._authorize() is None:
                return
            self._json({"error": "method not allowed"}, 501)
            return
        super().send_error(code, message, explain)

    def log_message(self, fmt, *args):
        pass  # 静默访问日志


def _wait_for_jobs(timeout: float = 120) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with _JOBS_LOCK:
            if not any(j.get("status") == "running" for j in _JOBS.values()):
                return True
        time.sleep(0.1)
    return False


class AuthHTTPServer(ThreadingHTTPServer):
    """把认证管理器挂在 server 实例上，测试可直接注入独立 AuthManager。"""

    def __init__(self, server_address, handler_class, auth: security.AuthManager):
        self.auth = auth
        self.state_lock = threading.Lock()
        self.shutting_down = False
        super().__init__(server_address, handler_class)


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8787)
    a = ap.parse_args(argv)
    try:
        auth = security.load_manager()
    except Exception as e:
        raise SystemExit(f"启动失败：{e}") from e

    srv = AuthHTTPServer(("127.0.0.1", a.port), Handler, auth)

    def stop(_signum, _frame):
        # 信号回调位于主线程；先拒绝新任务并等待已登记任务，再由其他线程停机。
        with srv.state_lock:
            srv.shutting_down = True

        def stop_later():
            if not _wait_for_jobs():
                print("后台任务等待超时，安全退出", file=sys.stderr)
            srv.shutdown()

        threading.Thread(target=stop_later, daemon=True).start()

    previous_handlers = {}
    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            previous_handlers[sig] = signal.signal(sig, stop)
        except (OSError, ValueError):
            pass  # 非主线程或平台不支持该信号
    try:
        print(f"AI 价格归一化计算器 → http://127.0.0.1:{a.port}")
        print("接口: /api/auth/login /api/auth/logout /api/models /api/config "
              "/api/fx /api/nodes /api/pricing/update /api/jobs/<id>")
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        srv.server_close()
        for sig, handler in previous_handlers.items():
            signal.signal(sig, handler)


if __name__ == "__main__":
    main()
