"""v2rayN 节点池：读取节点库 → 独立拉起 xray / sing-box 本地端口 → 探测目标可用性。

设计约束（用户明确要求）：
- 只读 v2rayN 数据库与二进制，绝不修改其配置，也不依赖其正在运行的代理端口。
- 每个候选节点临时起一个本地 HTTP 代理端口，用完即杀进程、删临时配置。
- 节点策略：VLESS+Reality 优先，其次 Hysteria2，其他协议跳过（GFW 现状）。
- 节点凭据只出现在临时文件与内存中，日志与返回值一律不输出敏感字段。
"""
from __future__ import annotations

import json
import os
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field

V2RAYN_DIR = os.path.join(os.environ.get("LOCALAPPDATA", ""), "v2rayN")
DB_PATH = os.path.join(V2RAYN_DIR, "guiConfigs", "guiNDB.db")
XRAY_EXE = os.path.join(V2RAYN_DIR, "bin", "xray", "xray.exe")
SINGBOX_EXE = os.path.join(V2RAYN_DIR, "bin", "sing_box", "sing-box.exe")

CFG_VLESS = 5   # ProfileItem.ConfigType
CFG_HY2 = 7

_NO_WINDOW = getattr(subprocess, "CREATE_NO_WINDOW", 0)


@dataclass
class Node:
    index_id: str
    name: str
    kind: str            # "vless-reality" | "hysteria2"
    delay: int           # v2rayN 测速 ms；-1/0 表示失败或未测
    row: dict = field(repr=False)   # 含敏感字段，勿打印

    @property
    def label(self) -> str:
        return f"{self.kind}:{self.name or self.index_id[-6:]}"


def _load_candidates(db_path: str = DB_PATH) -> list[Node]:
    db = sqlite3.connect(db_path)
    db.row_factory = sqlite3.Row
    delays = {r["IndexId"]: (r["Delay"] or -1)
              for r in db.execute("SELECT IndexId, Delay FROM ProfileExItem")}
    nodes: list[Node] = []
    for r in db.execute("SELECT * FROM ProfileItem WHERE IsSub=1 OR IsSub IS NULL OR IsSub=0"):
        row = dict(r)
        ct, sec = row.get("ConfigType"), (row.get("StreamSecurity") or "").lower()
        if ct == CFG_VLESS and sec == "reality":
            kind = "vless-reality"
        elif ct == CFG_HY2:
            kind = "hysteria2"
        else:
            continue
        nodes.append(Node(index_id=str(row["IndexId"]), name=row.get("Remarks") or "",
                          kind=kind, delay=delays.get(str(row["IndexId"]), -1), row=row))
    db.close()
    # vless-reality 组在前；组内按 v2rayN 实测延迟升序，未测/失败排最后
    order = {"vless-reality": 0, "hysteria2": 1}
    nodes.sort(key=lambda n: (order[n.kind], n.delay if n.delay > 0 else 1 << 20))
    return nodes


def _xray_vless_config(node: Node, port: int) -> dict:
    r = node.row
    user = {"id": r.get("Id") or "", "encryption": r.get("Security") or "none"}
    if r.get("Flow"):
        user["flow"] = r["Flow"]
    stream = {
        "network": r.get("Network") or "tcp",
        "security": "reality",
        "realitySettings": {
            "serverName": r.get("Sni") or "",
            "fingerprint": r.get("Fingerprint") or "chrome",
            "publicKey": r.get("PublicKey") or "",
            "shortId": r.get("ShortId") or "",
            "spiderX": r.get("SpiderX") or "",
        },
    }
    net = stream["network"]
    if net == "tcp":
        stream["tcpSettings"] = {"header": {"type": r.get("HeaderType") or "none"}}
    elif net == "ws":
        stream["wsSettings"] = {"path": r.get("Path") or "/",
                                "headers": {"Host": r.get("RequestHost") or ""}}
    elif net == "grpc":
        stream["grpcSettings"] = {"serviceName": r.get("Path") or ""}
    elif net == "xhttp":
        stream["xhttpSettings"] = {"path": r.get("Path") or "/",
                                   "host": r.get("RequestHost") or ""}
    return {
        "log": {"loglevel": "warning"},
        "inbounds": [{"listen": "127.0.0.1", "port": port,
                      "protocol": "http", "settings": {}}],
        "outbounds": [{
            "protocol": "vless",
            "settings": {"vnext": [{"address": r["Address"],
                                    "port": int(r["Port"]), "users": [user]}]},
            "streamSettings": stream,
        }],
    }


def _singbox_hy2_config(node: Node, port: int) -> dict:
    r = node.row
    try:
        proto = json.loads(r.get("ProtoExtra") or "{}")
    except json.JSONDecodeError:
        proto = {}
    outbound = {
        "type": "hysteria2",
        "server": r["Address"],
        "server_port": int(r["Port"]),
        "password": r.get("Password") or "",
        "tls": {
            "enabled": True,
            "server_name": r.get("Sni") or "",
            "insecure": str(r.get("AllowInsecure") or "").lower() == "true",
        },
    }
    if proto.get("SalamanderPass"):
        outbound["obfs"] = {"type": "salamander", "password": proto["SalamanderPass"]}
    if proto.get("Ports"):  # 端口跳跃，"443-449" → sing-box 格式
        spec = str(proto["Ports"]).replace("-", ":")
        outbound["server_ports"] = [p.strip() for p in spec.split(",") if p.strip()]
    return {
        "log": {"level": "error"},
        "inbounds": [{"type": "mixed", "tag": "in",
                      "listen": "127.0.0.1", "listen_port": port}],
        "outbounds": [outbound],
    }


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _wait_listen(port: int, proc: subprocess.Popen, timeout: float = 4.0) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if proc.poll() is not None:
            return False
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.3):
                return True
        except OSError:
            time.sleep(0.1)
    return False


def fetch_url(url: str, proxy_port: int | None, timeout: float = 10.0) -> dict:
    """经指定本地代理端口抓取，返回 {status, bytes, ms, error}。

    proxy_port=None → 严格直连：显式空 ProxyHandler 关闭 urllib 对
    HTTP(S)_PROXY 环境变量的隐式读取（显式原则，勿移除）。"""
    handlers = [urllib.request.ProxyHandler(
        {"http": f"http://127.0.0.1:{proxy_port}",
         "https": f"http://127.0.0.1:{proxy_port}"})] if proxy_port \
        else [urllib.request.ProxyHandler({})]
    opener = urllib.request.build_opener(*handlers)
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"})
    t0 = time.time()
    try:
        with opener.open(req, timeout=timeout) as resp:
            body = resp.read()
        return {"status": resp.status, "bytes": len(body),
                "ms": round((time.time() - t0) * 1000), "body": body}
    except urllib.error.HTTPError as e:
        return {"status": e.code, "bytes": 0,
                "ms": round((time.time() - t0) * 1000), "error": f"HTTP {e.code}"}
    except Exception as e:  # URLError / timeout / TLS 等
        return {"status": 0, "bytes": 0,
                "ms": round((time.time() - t0) * 1000), "error": type(e).__name__}


class _LocalProxy:
    """单个节点临时本地代理的上下文管理器。"""

    def __init__(self, node: Node):
        self.node = node
        self.port = _free_port()
        self.proc: subprocess.Popen | None = None
        self._tmp: str | None = None

    def __enter__(self) -> "_LocalProxy":
        if self.node.kind == "vless-reality":
            exe, cfg = XRAY_EXE, _xray_vless_config(self.node, self.port)
        else:
            exe, cfg = SINGBOX_EXE, _singbox_hy2_config(self.node, self.port)
        fd, self._tmp = tempfile.mkstemp(suffix=".json", prefix="aicfg_")
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(cfg, f)
        self.proc = subprocess.Popen(
            [exe, "run", "-c", self._tmp],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            creationflags=_NO_WINDOW)
        if not _wait_listen(self.port, self.proc):
            raise RuntimeError(f"{self.node.label} 本地端口未就绪")
        return self

    def __exit__(self, *_):
        if self.proc:
            self.proc.kill()
            try:
                self.proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                pass
        if self._tmp:
            try:
                os.remove(self._tmp)
            except OSError:
                pass


def probe_node(node: Node, urls: dict[str, str], timeout: float = 8.0,
               validate=None) -> dict:
    """单节点探测一组 URL，返回 {label, kind, results: {key: {...}}}。

    validate(key, body)->bool 非 None 时对 200 响应做内容校验（如"能解析出
    价目行"），校验不过记 error 不当可用——地区限制/反爬页也是 200，只看
    状态码会误判节点可用。
    """
    out = {"node": node.label, "kind": node.kind, "results": {}}
    try:
        with _LocalProxy(node) as lp:
            out["proxy"] = f"http://127.0.0.1:{lp.port}"
            for key, url in urls.items():
                r = fetch_url(url, lp.port, timeout=timeout)
                if (validate and r.get("status") == 200
                        and not validate(key, r.get("body") or b"")):
                    r["error"] = "返回内容校验未通过（疑似反爬/地区限制页）"
                out["results"][key] = r  # body 保留：胜出者的已校验内容直接复用
    except Exception as e:
        out["error"] = f"{type(e).__name__}: {e}"
    return out


def find_working_proxy(urls: dict[str, str], max_nodes: int = 30,
                       workers: int = 4, timeout: float = 8.0,
                       db_path: str = DB_PATH, validate=None,
                       on_progress=None) -> dict:
    """按候选顺序探测节点，为每个目标 URL 找首个可用节点。

    返回 {winners: {key: {node, proxy}}, report: [...]}。
    注意：winners 里的本地端口进程已退出，仅记录哪个节点可用；
    实际抓取需用 ensure_proxy() 重新拉起该节点。
    """
    nodes = _load_candidates(db_path)[:max_nodes]
    pending = dict(urls)
    winners: dict[str, dict] = {}
    report = []
    for i in range(0, len(nodes), workers):
        if not pending:
            break
        batch = nodes[i:i + workers]
        with ThreadPoolExecutor(max_workers=workers) as ex:
            futs = {ex.submit(probe_node, n, pending, timeout, validate): n
                    for n in batch}
            for fut in as_completed(futs):
                n = futs[fut]
                try:
                    res = fut.result()
                except Exception as e:
                    res = {"node": n.label, "error": str(e), "results": {}}
                report.append(res)
                for key, r in res.get("results", {}).items():
                    if (key in pending and r.get("status") == 200
                            and not r.get("error") and key not in winners):
                        winners[key] = {"node": res["node"], "node_id": n.index_id,
                                        "body": r.get("body", b"")}
                    r.pop("body", None)  # report 不保留 body，胜出者已摘出
        for key in list(pending):
            if key in winners:
                pending.pop(key)
        if on_progress:
            on_progress({"tested": len(report), "pending": list(pending)})
    return {"winners": winners, "report": report, "pending": list(pending)}


def proxy_through_node(node_id: str, db_path: str = DB_PATH):
    """按 IndexId 找回节点并进入其本地代理上下文，供抓取复用。"""
    for n in _load_candidates(db_path):
        if n.index_id == node_id:
            return _LocalProxy(n)
    raise KeyError(f"节点 {node_id} 不在候选集（仅 VLESS+Reality / Hysteria2）")


if __name__ == "__main__":
    import sys
    nodes = _load_candidates()
    print(f"候选节点 {len(nodes)} 个（vless-reality "
          f"{sum(1 for n in nodes if n.kind == 'vless-reality')}，hy2 "
          f"{sum(1 for n in nodes if n.kind == 'hysteria2')}）")
    targets = {
        "xai": "https://docs.x.ai/developers/pricing.md",
        "openai": "https://developers.openai.com/api/docs/pricing.md",
        "anthropic": "https://platform.claude.com/docs/en/about-claude/pricing.md",
        "google": "https://aistudio.google.com/docs/pricing.md",
    }
    max_n = int(sys.argv[1]) if len(sys.argv) > 1 else 10
    print(f"探测前 {max_n} 个节点...")
    res = find_working_proxy(targets, max_nodes=max_n)
    print("可用节点:", json.dumps(res["winners"], ensure_ascii=False, indent=1))
    for r in res["report"]:
        line = " | ".join(f"{k}={v.get('status') or v.get('error', '?')}"
                          for k, v in r.get("results", {}).items())
        print(f"  {r['node']:40s} {r.get('error','')} {line}")
