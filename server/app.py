"""轻量后端：静态文件 + 配置读写 + 汇率 + 价目更新任务。

零依赖（Python 3.10+ 标准库）。启动：
    python -m server.app [--port 8787]
接口：
    GET  /                     → public/index.html
    GET  /api/models           → data/models.json
    GET  /api/config           → data/config.json（不存在则返回默认）
    PUT  /api/config           → 写回 data/config.json
    GET  /api/fx               → {rate, source, fetchedAt, cached}
    POST /api/pricing/update   → {"jobId": "..."} 后台跑 fetcher.update
    POST /api/nodes/test       → {"jobId": "..."} 后台跑节点连通性探测
    GET  /api/jobs/<id>        → {"status": running|done|error, "report": ...}
"""
from __future__ import annotations

import argparse
import json
import os
import threading
import time
import uuid
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from . import fetcher, nodepool

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PUBLIC = os.path.join(ROOT, "public")
DATA = os.path.join(ROOT, "data")
CONFIG_PATH = os.path.join(DATA, "config.json")
FX_CACHE = os.path.join(DATA, "fx-cache.json")
FX_URL = "https://open.er-api.com/v6/latest/USD"
FX_TTL = 24 * 3600

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
    try:
        rep = fetcher.update(providers, max_nodes=max_nodes,
                             log=lambda m: lines.append(str(m)))
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

    def _body(self) -> dict:
        n = int(self.headers.get("Content-Length") or 0)
        if not n:
            return {}
        try:
            return json.loads(self.rfile.read(n))
        except json.JSONDecodeError:
            return {}

    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET,PUT,POST,OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    def do_GET(self):
        path = self.path.split("?")[0]
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
            job = _JOBS.get(path.rsplit("/", 1)[-1])
            self._json(job or {"error": "not found"}, 200 if job else 404)
        else:
            self._static(path)

    def _static(self, path: str):
        rel = "index.html" if path in ("/", "") else path.lstrip("/")
        fp = os.path.normpath(os.path.join(PUBLIC, rel))
        if not fp.startswith(PUBLIC) or not os.path.isfile(fp):
            return self._send(404, b"not found", "text/plain")
        with open(fp, "rb") as f:
            self._send(200, f.read(), _MIME.get(os.path.splitext(fp)[1],
                                              "application/octet-stream"))

    def do_PUT(self):
        if self.path.split("?")[0] == "/api/config":
            cfg = self._body()
            os.makedirs(DATA, exist_ok=True)
            tmp = CONFIG_PATH + ".tmp"
            with open(tmp, "w", encoding="utf-8") as f:
                json.dump(cfg, f, ensure_ascii=False, indent=2)
                f.flush()
                os.fsync(f.fileno())
            os.replace(tmp, CONFIG_PATH)   # 同卷原子替换，写一半被kill也不会留坏文件
            self._json({"ok": True})
        else:
            self._send(404, b"not found", "text/plain")

    def do_POST(self):
        path = self.path.split("?")[0]
        if path == "/api/pricing/update":
            body = self._body()
            job_id = uuid.uuid4().hex[:8]
            with _JOBS_LOCK:
                _JOBS[job_id] = {"status": "running", "report": None, "log": []}
            threading.Thread(
                target=_run_update,
                args=(job_id, body.get("providers"), int(body.get("maxNodes", 25))),
                daemon=True).start()
            self._json({"jobId": job_id})
        elif path == "/api/nodes/test":
            body = self._body()
            job_id = uuid.uuid4().hex[:8]
            with _JOBS_LOCK:
                _JOBS[job_id] = {"status": "running", "report": None, "log": []}
            threading.Thread(
                target=_run_nodes_test,
                args=(job_id, bool(body.get("force")),
                      int(body.get("maxNodes", 25))),
                daemon=True).start()
            self._json({"jobId": job_id})
        else:
            self._send(404, b"not found", "text/plain")

    def log_message(self, fmt, *args):
        pass  # 静默访问日志


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8787)
    a = ap.parse_args()
    srv = ThreadingHTTPServer(("127.0.0.1", a.port), Handler)
    print(f"AI 价格归一化计算器 → http://127.0.0.1:{a.port}")
    print("接口: /api/models /api/config /api/fx /api/nodes "
          "/api/pricing/update /api/jobs/<id>")
    srv.serve_forever()


if __name__ == "__main__":
    main()
