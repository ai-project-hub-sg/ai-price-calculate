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
import threading
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

# ---------------------------------------------------------------- 可用性缓存
# 进程内 1h TTL（对齐 Go proxypool.Cache）：探测结果（ok/ms/err）+「测试节点」
# 完整结果快照。find_working_proxy 温路径直接复用——Python 每测一个节点要起
# 一次 xray/sing-box 子进程，重探比 Go 版更贵，缓存收益更大。
_CACHE_TTL = 3600.0
PROBE_URL = "https://www.gstatic.com/generate_204"  # 与 Go 版同一连通性目标

# maxNodes 上限规则（CR-005，双端同源：fetcher/app/nodepool 共用）：
# 超过 MAX_NODES_WARN 提示「是否输入出错」但不拒绝；超过 MAX_NODES_LIMIT
# 一律拒绝（极限大值只可能是输错——节点池现实规模远低于 999）。
MAX_NODES_WARN = 999
MAX_NODES_LIMIT = 9999
_NODE_CACHE: dict = {"usable": {}, "test": None}
# usable: index_id -> {"ok":bool,"ms":int,"err":str,"at":float}
# test:   {"at":float, "results":[{id,name,kind,ok,ms,error}]}


def _cache_mark(node: Node, ok: bool, ms: int = 0, err: str = "") -> None:
    _NODE_CACHE["usable"][node.index_id] = {
        "ok": ok, "ms": ms, "err": err, "at": time.time()}


def _cache_usable(nodes: list[Node]) -> list[Node]:
    now = time.time()
    return [n for n in nodes
            if (s := _NODE_CACHE["usable"].get(n.index_id))
            and s["ok"] and now - s["at"] < _CACHE_TTL]


def cache_test_results() -> tuple[list[dict], float] | None:
    """TTL 内「测试节点」结果快照（秒回用）；无/过期返回 None。"""
    t = _NODE_CACHE["test"]
    if t and time.time() - t["at"] < _CACHE_TTL:
        return list(t["results"]), t["at"]
    return None


def mark_test_done(results: list[dict]) -> None:
    _NODE_CACHE["test"] = {"at": time.time(), "results": list(results)}


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
    # v2rayN 7.x schema：UUID 存 Password（Id 列为空）、flow/encryption 挪进
    # ProtoExtra JSON、transport 细节挪进 TransportExtra JSON——全部新旧兼容读取。
    try:
        proto = json.loads(r.get("ProtoExtra") or "{}")
    except json.JSONDecodeError:
        proto = {}
    try:
        tex = json.loads(r.get("TransportExtra") or "{}")
    except json.JSONDecodeError:
        tex = {}
    user = {"id": r.get("Id") or r.get("Password") or "",
            "encryption": r.get("Security") or proto.get("VlessEncryption") or "none"}
    flow = r.get("Flow") or proto.get("Flow")
    if flow:
        user["flow"] = flow
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
    hdr = r.get("HeaderType") or tex.get("RawHeaderType") or "none"
    path = r.get("Path") or tex.get("Path") or ""
    host = r.get("RequestHost") or tex.get("Host") or ""
    if net == "tcp":
        stream["tcpSettings"] = {"header": {"type": hdr}}
    elif net == "raw":  # xray ≥1.8 把 tcp 改名为 raw，配置键同步改名
        stream["rawSettings"] = {"header": {"type": hdr}}
    elif net == "ws":
        stream["wsSettings"] = {"path": path or "/",
                                "headers": {"Host": host}}
    elif net == "grpc":
        stream["grpcSettings"] = {"serviceName": path}
    elif net == "xhttp":
        stream["xhttpSettings"] = {"path": path or "/", "host": host}
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
    HTTP(S)_PROXY 环境变量的隐式读取（显式原则，勿移除）。

    硬上限 = timeout*1.5+2：urllib 的 timeout 是 per-op，DNS/多 IP 顺序重试
    会累计超标（google 直连曾卡 70s）。超限时 daemon 线程被放弃（进程级
    无法强杀线程，泄漏到自然结束，代价可接受）。"""
    cap = timeout * 1.5 + 2
    box: dict = {}

    def _go():
        box.update(_fetch_inner(url, proxy_port, timeout))

    t0 = time.time()
    th = threading.Thread(target=_go, daemon=True)
    th.start()
    th.join(cap)
    if th.is_alive():
        return {"status": 0, "bytes": 0,
                "ms": round((time.time() - t0) * 1000),
                "error": f"timeout(hard {cap:.0f}s)"}
    return box


def _fetch_inner(url: str, proxy_port: int | None, timeout: float) -> dict:
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
        if not os.path.isfile(exe):
            raise RuntimeError(
                f"找不到 {os.path.basename(exe)}（{exe}）——检查 v2rayN 目录")
        fd, self._tmp = tempfile.mkstemp(suffix=".json", prefix="aicfg_")
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(cfg, f)
        self.proc = subprocess.Popen(
            [exe, "run", "-c", self._tmp],
            stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True,
            creationflags=_NO_WINDOW)
        if not _wait_listen(self.port, self.proc):
            tail = ""
            try:
                self.proc.kill()
                _, tail = self.proc.communicate(timeout=2)
                lines = (tail or "").strip().splitlines()
                tail = lines[-1][:160] if lines else ""
            except Exception:
                pass
            raise RuntimeError(f"{self.node.label} 本地端口未就绪"
                               + (f"：{tail}" if tail else ""))
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
               validate=None, on_target=None, on_target_done=None) -> dict:
    """单节点探测一组 URL，返回 {label, kind, results: {key: {...}}}。

    validate(key, body)->bool 非 None 时对 200 响应做内容校验（如"能解析出
    价目行"），校验不过记 error 不当可用——地区限制/反爬页也是 200，只看
    状态码会误判节点可用。
    on_target(key) / on_target_done(key) 开始/结束抓某目标回调——
    组合进度（节点→厂家）与在飞集合用。
    """
    out = {"node": node.label, "kind": node.kind, "results": {}}
    try:
        with _LocalProxy(node) as lp:
            out["proxy"] = f"http://127.0.0.1:{lp.port}"
            for key, url in urls.items():
                if on_target:
                    on_target(key)
                r = fetch_url(url, lp.port, timeout=timeout)
                if on_target_done:
                    on_target_done(key)
                if (validate and r.get("status") == 200
                        and not validate(key, r.get("body") or b"")):
                    r["error"] = "返回内容校验未通过（疑似反爬/地区限制页）"
                out["results"][key] = r  # body 保留：胜出者的已校验内容直接复用
    except Exception as e:
        out["error"] = f"{type(e).__name__}: {e}"
    return out


def find_working_proxy(urls: dict[str, str], max_nodes: int = 999,
                       workers: int = 4, timeout: float = 8.0,
                       db_path: str = DB_PATH, validate=None,
                       on_progress=None) -> dict:
    """按候选顺序探测节点，为每个目标 URL 找首个可用节点。

    返回 {winners: {key: {node, node_id, body}}, report: [...]}。
    body 为探测时已校验的响应体，调用方直接复用（重抓有内容抖动风险）。

    温路径：1h 内测过的可用节点先依序复用（不跑批探测）；耗尽才冷探
    本次未试过的节点。所有探测结果回写进程内可用性缓存。
    注意：传给 probe_node 的必须是 dict(pending) 快照——live dict 会在
    兄弟线程迭代时 pop 导致 RuntimeError。
    """
    # 上限语义与其余三条路径一致：非正整数不封顶（API 层已拒绝 0/负数，
    # 此处只是库层语义统一，防止内部调用走上 nodes[:0]=空 的岔路）
    nodes = _load_candidates(db_path)
    if max_nodes > 0:
        nodes = nodes[:max_nodes]
    pending = dict(urls)
    winners: dict[str, dict] = {}
    report = []
    tried: set[str] = set()

    def _mk_hooks(n: Node, phase: str, idx: int, total: int):
        """节点开抓/抓完某目标的成对事件（inflight 集合 + 组合进度）。"""
        if not on_progress:
            return None, None

        def start(key):
            on_progress({"phase": phase, "node": n.label, "target": key,
                         "inflight": True, "idx": idx, "total": total,
                         "tested": len(report), "pending": list(pending)})

        def end(key):
            on_progress({"phase": phase, "node": n.label, "target": key,
                         "inflight": False, "idx": idx, "total": total,
                         "tested": len(report), "pending": list(pending)})

        return start, end

    cur_phase = ["warm"]  # absorb 胜出事件用的阶段标签（warm→probe 切换）

    def absorb(res: dict, n: Node) -> None:
        report.append(res)
        for key, r in res.get("results", {}).items():
            if (key in pending and r.get("status") == 200
                    and not r.get("error") and key not in winners):
                winners[key] = {"node": res["node"], "node_id": n.index_id,
                                "body": r.get("body", b"")}
                if on_progress:  # 胜出瞬间事件：done 立刻变绿，不等汇总
                    on_progress({"phase": cur_phase[0], "node": res["node"],
                                 "target": key, "won": True})
            r.pop("body", None)  # report 不保留 body，胜出者已摘出
        for key in list(pending):
            if key in winners:
                pending.pop(key)
        # 回写缓存：启动失败/全超时记不可用；拿到过任何 HTTP 响应记可用
        # （被墙页也是响应——链路通，只是内容不对目标）。
        if res.get("error"):
            _cache_mark(n, False, err=res["error"])
        else:
            oks = [r for r in res.get("results", {}).values() if r.get("status")]
            if oks:
                _cache_mark(n, True, ms=min(r.get("ms", 0) for r in oks))
            else:
                _cache_mark(n, False, err="全部目标无响应")

    # 温路径：缓存可用节点按候选序逐个「抓+校验」待解目标
    warm = _cache_usable(nodes)
    for wi, n in enumerate(warm):
        if not pending:
            break
        tried.add(n.index_id)
        if on_progress:
            on_progress({"phase": "warm", "node": n.label, "idx": wi + 1,
                         "total": len(warm), "tested": len(report),
                         "pending": list(pending)})
        try:
            st, en = _mk_hooks(n, "warm", wi + 1, len(warm))
            absorb(probe_node(n, dict(pending), timeout, validate, st, en), n)
        except Exception as e:
            absorb({"node": n.label, "error": str(e), "results": {}}, n)
        if on_progress:
            on_progress({"tested": len(report), "pending": list(pending)})

    # 冷路径：批量探测本次未试过的节点
    cur_phase[0] = "probe"
    rest = [n for n in nodes if n.index_id not in tried]
    for i in range(0, len(rest), workers):
        if not pending:
            break
        batch = rest[i:i + workers]
        snapshot = dict(pending)
        if on_progress:
            for j, n in enumerate(batch):  # 节点开测事件（并发批，序号=开测序）
                on_progress({"phase": "probe", "node": n.label, "idx": i + j + 1,
                             "total": len(rest), "tested": len(report),
                             "pending": list(pending)})
        with ThreadPoolExecutor(max_workers=workers) as ex:
            futs = {}
            for j, n in enumerate(batch):
                st, en = _mk_hooks(n, "probe", i + j + 1, len(rest))
                futs[ex.submit(probe_node, n, snapshot, timeout, validate,
                               st, en)] = n
            for fut in as_completed(futs):
                n = futs[fut]
                try:
                    res = fut.result()
                except Exception as e:
                    res = {"node": n.label, "error": str(e), "results": {}}
                absorb(res, n)
        if on_progress:
            on_progress({"tested": len(report), "pending": list(pending)})
    return {"winners": winners, "report": report, "pending": list(pending)}

# on_progress 事件：{"phase","node","idx","total",...}=节点开测；
# {"phase","node","target","inflight"}=目标开抓/抓完；{"phase","node",
# "target","won"}=胜出瞬间；{"tested","pending"}=批次完成。


def test_nodes(nodes: list[Node], max_nodes: int = 999, workers: int = 4,
               timeout: float = 10.0, on_progress=None) -> list[dict]:
    """连通性探测（对应 Go TestNodes）：每节点起本地端口拉一次 PROBE_URL，
    2xx 判可用。结果写入进程内缓存：find_working 温路径复用 +
    「测试节点」按钮 1h 内秒回。"""
    probed = nodes[:max_nodes] if max_nodes > 0 else nodes
    results: list[dict] = [None] * len(probed)
    for i in range(0, len(probed), workers):
        batch = probed[i:i + workers]
        with ThreadPoolExecutor(max_workers=workers) as ex:
            futs = {ex.submit(probe_node, n, {"_": PROBE_URL}, timeout): j
                    for j, n in enumerate(batch, start=i)}
            for fut in as_completed(futs):
                j, n = futs[fut], probed[futs[fut]]
                r = {"id": n.index_id, "name": n.label, "kind": n.kind,
                     "ok": False, "ms": 0}
                try:
                    rep = fut.result()
                    res = rep.get("results", {}).get("_")
                    if rep.get("error"):
                        r["error"] = rep["error"]
                    elif res:
                        r["ms"] = res.get("ms", 0)
                        if 200 <= res.get("status", 0) < 300:
                            r["ok"] = True
                        else:
                            r["error"] = res.get("error") or f"HTTP {res.get('status')}"
                except Exception as e:
                    r["error"] = f"{type(e).__name__}: {e}"
                _cache_mark(n, r["ok"], r["ms"], r.get("error", ""))
                results[j] = r
        if on_progress:
            on_progress({"tested": i + len(batch), "total": len(probed)})
    mark_test_done(results)
    return results


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
    if max_n > MAX_NODES_LIMIT:
        sys.exit(f"节点数上限 {MAX_NODES_LIMIT}，收到 {max_n}——是否输入出错？")
    if max_n > MAX_NODES_WARN:
        print(f"警告：探测 {max_n} 个节点超过常用上限 {MAX_NODES_WARN}——是否输入出错？")
    print(f"探测前 {max_n} 个节点...")
    res = find_working_proxy(targets, max_nodes=max_n)
    print("可用节点:", json.dumps(res["winners"], ensure_ascii=False, indent=1))
    for r in res["report"]:
        line = " | ".join(f"{k}={v.get('status') or v.get('error', '?')}"
                          for k, v in r.get("results", {}).items())
        print(f"  {r['node']:40s} {r.get('error','')} {line}")
