"""官方价目抓取与合并。

- 只对接各厂商官方页面（.md 纯文本版优先），不使用第三方聚合价目。
- 可自动抓取的厂商：xAI、DeepSeek、Z.AI（国际）、智谱 bigmodel.cn（国内）。
- 其余厂商（OpenAI/Anthropic/Google/Kimi）做连通性探测并给出人工核对清单——
  页面可达但解析策略未实现时，绝不静默编造价格。
- 直连失败时经 server.nodepool 的 v2rayN 节点池重试（VLESS+Reality → HY2）。
- 合并只更新 models.json 中厂商价目（tiers/fetchedAt/checkedAt），
  从不写入 data/config.json —— 用户自定义折扣/覆盖不受影响。

CLI：  python -m server.fetcher [--providers xai,zai] [--max-nodes 25] [--dry-run]
"""
from __future__ import annotations

import argparse
import datetime as _dt
import json
import os
import re
import shutil
import sys
import threading
import time

from . import nodepool

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODELS_PATH = os.path.join(ROOT, "data", "models.json")

TODAY = _dt.date.today().isoformat()

# 判定"页面真实抓到"的最小字节数——Kimi 等 JS 渲染页返回的空壳 .md
# 只有几百 B，低于此阈值视为未抓到（显式阈值，勿静默改）。
MIN_USEFUL_BODY = 500


# ---------------------------------------------------------------- 解析工具

def _cells(line: str) -> list[str]:
    return [c.strip() for c in line.strip().strip("|").split("|")]


def _num(s: str):
    m = re.search(r"([0-9]+(?:\.[0-9]+)?)", s.replace(",", ""))
    return float(m.group(1)) if m else None


def _tokens(s: str):
    """'500k'/'1M'/'32K' → token 数。"""
    m = re.search(r"([0-9]+(?:\.[0-9]+)?)\s*([kKmM])", s)
    if not m:
        return None
    return int(float(m.group(1)) * (1000 if m.group(2).lower() == "k" else 1_000_000))


def _rows(md: str) -> list[list[str]]:
    out = []
    for l in md.splitlines():
        s = l.strip().lstrip(">").strip()   # 兼容引用块里的表格 "> | … |"
        if s.startswith("|") and "---" not in s:
            out.append(_cells(s))
    return out


def _money(s: str):
    """'$1.4' / '\\$0.26' / '免费' / '不支持' → float|0|None。"""
    if "免费" in s or s.strip() in ("Free", "free"):
        return 0.0
    if "不支持" in s or s.strip() in ("-", "—"):
        return None
    return _num(s)


def _mk_tiers(rows: list[tuple[int | None, float, float, float | None]]) -> list[dict]:
    return [{"upToTokens": up, "input": i, "output": o,
             **({"cacheRead": c} if c is not None else {})}
            for up, i, o, c in rows]


# ---------------------------------------------------------------- 各厂商解析器

def parse_xai(md: str) -> list[dict]:
    """| grok-4.7 (< 200k prompt tokens) | 500k | $2.00 | $0.50 | $6.00 |"""
    models: dict[str, dict] = {}
    for c in _rows(md):
        if len(c) < 5:
            continue
        m = re.search(r"(?i)^(grok-[\w.\-]+)\s*\((<|≥)\s*([\d.]+)\s*k", c[0])  # 显式 ^ 锚定（勿用 re.match 隐式锚定）
        if not m:
            continue
        mid, direction, thr = m.group(1), m.group(2), _tokens(m.group(3) + "k")
        ent = models.setdefault(mid, {"id": mid, "name": mid,
                                      "contextWindow": _tokens(c[1]), "tiers": []})
        ent["tiers"].append({
            "upToTokens": (thr - 1) if direction == "<" else None,
            "input": _money(c[2]), "cacheRead": _money(c[3]), "output": _money(c[4])})
    out = []
    for ent in models.values():
        tiers = sorted(ent["tiers"], key=lambda t: t["upToTokens"] is None)
        ent["tiers"] = [{"upToTokens": t["upToTokens"], "input": t["input"],
                         "output": t["output"], "cacheRead": t["cacheRead"]}
                        for t in tiers]
        out.append(ent)
    return out


def parse_zai(md: str) -> list[dict]:
    """| GLM-5.3 | \\$1.4 | \\$0.26 | Limited-time Free | \\$4.4 |"""
    out = []
    for c in _rows(md):
        if len(c) < 5 or not c[0].lower().startswith("glm-") or "Model" in c[0]:
            continue
        out.append({"id": c[0].lower(), "name": c[0], "tiers": [{
            "upToTokens": None, "input": _money(c[1]),
            "cacheRead": _money(c[2]), "output": _money(c[4])}]})
    return out


def parse_deepseek(md: str) -> list[dict]:
    """竖表：表头末列为模型名；PRICING 行按 OFF-PEAK/PEAK 给每列模型定价。"""
    rows = _rows(md)
    names = None
    for i, c in enumerate(rows):
        if len(c) >= 4 and c[0].upper() == "MODEL" and c[-1].lower().startswith("deepseek"):
            names = [re.sub(r"\(\d+\)", "", x).strip() for x in c[3:]]
            start = i
            break
    if not names:
        return []
    price = {n: {} for n in names}
    for c in rows[start:]:
        if len(c) < 3 + len(names) or not c[0].startswith("PRICING"):
            continue
        dim, phase = c[1].upper(), c[2].upper()
        for j, n in enumerate(names):
            price[n][(dim, phase)] = _money(c[3 + j])
    out = []
    for n, p in price.items():
        peak = lambda k: p.get((k, "PEAK"))
        off = lambda k: p.get((k, "OFF-PEAK"))
        ent = {"id": n, "name": n, "tiers": [{
            "upToTokens": None,
            "input": peak("1M INPUT TOKENS / (CACHE MISS)"),
            "cacheRead": peak("1M INPUT TOKENS / (CACHE HIT)"),
            "output": peak("1M OUTPUT TOKENS")}]}
        i_off, i_peak = off("1M INPUT TOKENS / (CACHE MISS)"), peak("1M INPUT TOKENS / (CACHE MISS)")
        if i_off is not None and i_peak:
            ent["offPeakRatio"] = round(i_off / i_peak, 4)
            # 官方脚注: "Peak hours are 01:00 - 04:00 and 06:00 - 10:00 UTC, Monday through Friday..."
            m = re.search(r"Peak hours are (.+?) UTC", md)
            if m:
                wins = re.findall(r"(\d{2}:\d{2})\s*-\s*(\d{2}:\d{2})", m.group(1))
                if wins:
                    ent["peakWindows"] = wins
                    ent["peakNote"] = "UTC 工作日；法定节假日全天闲时（按工作日近似）"
        out.append(ent)
    return out


def _section(md: str, title: str) -> str:
    """截取 `### <title>` 到下一个 `###`/`##` 之间的文本。"""
    m = re.search(rf"^#{{2,4}}\s+{re.escape(title)}\s*$", md, re.M)
    if not m:
        return ""
    end = re.search(r"^#{1,4}\s", md[m.end():], re.M)
    return md[m.end(): m.end() + end.start()] if end else md[m.end():]


def parse_openai(md: str) -> list[dict]:
    """仅取 `### Standard pricing data` 节的表（Batch/Flex/Fast/Ultrafast 表跳过）。
    11 列 = 短/长上下文档；3 列 = 单档。"""
    sec = _section(md, "Standard pricing data")
    out: dict[str, dict] = {}
    for c in _rows(sec):
        if len(c) < 4 or c[0] == "Model":
            continue
        name = re.sub(r"\s*\(<\d+K.*?\)\s*", "", c[0]).strip()
        if not re.search(r"^(gpt-|o\d|chatgpt|codex|computer|gpt-realtime|gpt-audio|gpt-image|whisper|tts|davinci|babbage|text-)", name):
            continue
        mid = name.lower()
        if len(c) >= 11:
            tiers = [
                {"upToTokens": 272000, "input": _money(c[1]), "output": _money(c[2]),
                 "cacheRead": _money(c[3]),
                 "cacheWrites": [{"ttl": "30m", "price": _money(c[4])},
                                 {"ttl": "12h", "price": _money(c[5])}]},
                {"upToTokens": None, "input": _money(c[6]), "output": _money(c[7]),
                 "cacheRead": _money(c[8]),
                 "cacheWrites": [{"ttl": "30m", "price": _money(c[9])},
                                 {"ttl": "12h", "price": _money(c[10])}]},
            ]
            for t in tiers:  # '-' 单元格清理
                t["cacheWrites"] = [w for w in t["cacheWrites"] if w["price"] is not None]
                if t["cacheRead"] is None:
                    t.pop("cacheRead")
                if not t["cacheWrites"]:
                    t.pop("cacheWrites")
        else:
            tiers = [{"upToTokens": None, "input": _money(c[1]),
                      "output": _money(c[2]),
                      **({"cacheRead": _money(c[3])} if _money(c[3]) is not None else {})}]
        out[mid] = {"id": mid, "name": name, "tiers": tiers}
    return list(out.values())


def parse_anthropic(md: str) -> list[dict]:
    """主表 6 列：Model | input | 5m write | 1h write | cache hits | output。
    Haiku 5.5 两行含 prompt 长度限定 → 两档。Batch 表只有 3 列天然被过滤。"""
    grouped: dict[str, dict] = {}
    for c in _rows(md):
        if len(c) < 6 or not c[0].startswith("Claude "):
            continue
        name = re.sub(r"\[([^\]]+)\]\([^)]*\)", r"\1", c[0])  # 去链接留文字
        status = "retired" if "retired" in name else \
                 "limited" if "limited" in name else "active"
        name = re.sub(r"\s*\(.*?\)\s*", " ", name).strip()
        up = None
        m = re.search(r"prompts up to ([\d,]+) tokens", c[0])
        if m:
            up = int(m.group(1).replace(",", ""))
        mid = "claude-" + name.replace("Claude ", "").lower() \
                           .replace(".", "-").replace(" ", "-")
        ent = grouped.setdefault(mid, {"id": mid, "name": name,
                                       "status": status, "bands": []})
        ent["bands"].append((up, c))
    out = []
    for ent in grouped.values():
        bands = sorted(ent["bands"], key=lambda b: b[0] is None)
        tiers = []
        for i, (up, c) in enumerate(bands):
            tiers.append({
                "upToTokens": up if len(bands) > 1 else None,
                "input": _money(c[1]),
                "cacheWrites": [{"ttl": "5m", "price": _money(c[2])},
                                {"ttl": "1h", "price": _money(c[3])}],
                "cacheRead": _money(c[4]),
                "output": _money(c[5])})
        out.append({"id": ent["id"], "name": ent["name"],
                    "status": ent["status"], "tiers": tiers})
    return out


def parse_google(md: str) -> list[dict]:
    """google.md 用 `## <Name> {% id="model-id" %}` 分节 + `{% table %}` 伪表：
    `* <字段> / * <免费档> / * <付费档>` 三行一组，`---` 分隔。
    提取 Standard tab 下 Input/Output/Context caching 的付费价；
    `prompts > 200k` 等限定出第二档；促销期记 notes。"""
    out = []
    for m in re.finditer(
            r"^##\s+(.+?)\s*\{%\s*id=\"([\w.\-]+)\"\s*%\}(.*?)(?=^##\s|\Z)",
            md, re.M | re.S):
        disp, mid, body = m.group(1).strip(), m.group(2), m.group(3)
        tab = re.search(r'\{%\s*tab title="Standard"[^%]*%\}(.*?)\{%\s*/tab', body, re.S)
        if not tab:
            continue
        txt = tab.group(1)
        # 行格式：* Input price\n* <free>\n* <paid>\n  ---
        fields: dict[str, str] = {}
        for rm in re.finditer(
                r"\*\s*(Input price[^\n]*|Output price[^\n]*|Context caching price[^\n]*)\n"
                r"\s*\*\s*[^\n]+\n"
                r"\s*\*\s*((?:[^\n]+(?:\n(?!\s*\*|\s*---)[^\n]+)*)?)", txt):
            key = "cache" if rm.group(1).startswith("Context") else rm.group(1).split(" ")[0].lower()
            fields[key] = re.sub(r"\s+", " ", rm.group(2)).strip()
        if "input" not in fields or "output" not in fields:
            continue

        def tiers_of(field: str):
            """付费单元格形如 `$2.00, prompts <= 200k tokens  $4.00, prompts > 200k tokens`
            或含促销 `$0.75 through December 31, 2026. $1.50 starting …`。
            返回 (阈值, 短档价, 长档价|None)；促销只取当前价。"""
            cell = fields.get(field, "")
            pairs = [(float(m.group(1)), (m.group(2) or "").lower())
                     for m in re.finditer(
                         r"\$(\d+(?:\.\d+)?)\s*,?\s*(prompts?\s*(?:<=|<|>|>=)\s*[\d,]+\s*k?)?",
                         cell, re.I)]
            short = next((v for v, q in pairs if "<" in q), None)
            long_p = next(((v, q) for v, q in pairs if ">" in q), None)
            if long_p:
                tm = re.search(r"([\d,]+)\s*k", long_p[1])
                thr = _tokens(tm.group(0)) if tm else None
                return thr, short if short is not None else pairs[0][0], long_p[0]
            return None, (pairs[0][0] if pairs else None), None

        ti, iv, iv2 = tiers_of("input")
        to, ov, ov2 = tiers_of("output")
        tc, cv, cv2 = tiers_of("cache")
        thr = ti or to or tc
        notes = []
        promo = re.search(r"through\s+([A-Za-z]+\s+\d+,\s*\d{4})", fields.get("input", ""))
        if promo:
            notes.append(f"促销价至 {promo.group(1)}，之后价格见官网")
        tiers = [{"upToTokens": None, "input": iv, "output": ov,
                  **({"cacheRead": cv} if cv is not None else {})}]
        if thr and iv2 is not None:
            tiers[0]["upToTokens"] = thr
            tiers.append({"upToTokens": None, "input": iv2,
                          "output": ov2 if ov2 is not None else ov,
                          **({"cacheRead": cv2} if cv2 is not None else
                             {"cacheRead": cv} if cv is not None else {})})
        if iv is None or ov is None:
            continue                          # 付费列无价的模型（如 gemini-decision）不入库
        ent = {"id": mid, "name": disp, "tiers": tiers}
        if notes:
            ent["notes"] = "；".join(notes)
        if "preview" in mid:
            ent["status"] = "preview"
        out.append(ent)
    return out


def parse_bigmodel(md: str) -> list[dict]:
    """| GLM-4.7 | 输入 \\[0, 32K)，输出 ≥0.2K | 3 | 14 | 限时免费 | 0.6 |
    复合分档按 promptTokens 近似：同输入区间内取“输出≥0.2K”行（更常见的档），
    其余档位差异在合并报告里提示，由用户人工确认。"""
    grouped: dict[str, dict] = {}
    for c in _rows(md):
        if len(c) < 6 or not re.search(r"(?i)^GLM-[\w.\-]+$", c[0]):
            continue
        name, ctx = c[0], c[1]
        mid = name.lower() + "-cn"
        price = (_money(c[2]), _money(c[3]), _money(c[5]))
        lo = re.search(r"输入(?:长度)?\s*\\?\[\s*(\d+)K?\s*,\s*(\d+)K?\s*\\?\)", ctx)
        single = re.match(r"^\s*(\d+)\s*([kKmM])\s*$", ctx)
        ent = grouped.setdefault(mid, {"id": mid, "name": f"{name}（国内）",
                                       "contextWindow": None, "bands": {}})
        if single:
            ent["contextWindow"] = _tokens(ctx)
            ent["bands"].setdefault(0, []).append((ctx, *price))
        elif lo:
            ent["bands"].setdefault(int(lo.group(1)) * 1000, []).append((ctx, *price))
        elif "≥" in ctx and (m2 := re.search(r"(\d+)K", ctx)):
            ent["bands"].setdefault(int(m2.group(1)) * 1000, []).append((ctx, *price))
        else:                                   # '—' 等无区间标记 → 单档
            ent["bands"].setdefault(0, []).append((ctx, *price))
    out = []
    for ent in grouped.values():
        bands = sorted(ent["bands"].items())
        tiers, notes = [], []
        for idx, (lo, cands) in enumerate(bands):
            pick = next((x for x in cands if "≥0.2K" in x[0] or "≥ 0.2K" in x[0]), cands[0])
            if len(cands) > 1:
                notes.append("存在按输出长度细分的档位，已取“输出≥0.2K”行：" +
                             ";".join(x[0] for x in cands))
            up = None if idx == len(bands) - 1 else bands[idx + 1][0] - 1
            tiers.append({"upToTokens": up, "input": pick[1],
                          "output": pick[2], "cacheRead": pick[3]})
        row = {"id": ent["id"], "name": ent["name"],
               "contextWindow": ent["contextWindow"], "tiers": tiers}
        if notes:
            row["notes"] = "；".join(notes)
        out.append(row)
    return out


# ---------------------------------------------------------------- 源定义

SOURCES = {
    "xai":        {"url": "https://docs.x.ai/developers/pricing.md",        "parse": parse_xai,      "currency": "USD"},
    "deepseek":   {"url": "https://api-docs.deepseek.com/quick_start/pricing.md", "parse": parse_deepseek, "currency": "USD"},
    "zai":        {"url": "https://docs.z.ai/guides/overview/pricing.md",   "parse": parse_zai,      "currency": "USD"},
    "bigmodel-cn": {"url": "https://docs.bigmodel.cn/cn/guide/start/pricing.md",  "parse": parse_bigmodel, "currency": "CNY"},
    # 以下三家页面在部分代理出口可达，达则可自动解析
    "openai":     {"url": "https://developers.openai.com/api/docs/pricing.md",    "parse": parse_openai, "currency": "USD"},
    "anthropic":  {"url": "https://platform.claude.com/docs/en/about-claude/pricing.md", "parse": parse_anthropic, "currency": "USD"},
    "google":     {"url": "https://aistudio.google.com/docs/pricing.md",          "parse": parse_google, "currency": "USD"},
    # kimi 价格表 JS 渲染，.md 为空壳 → 连通性探测 + 人工核对
    "moonshot":   {"url": "https://platform.kimi.com/docs/pricing/chat.md",       "parse": None, "currency": "CNY"},
}


# ---------------------------------------------------------------- 合并

def merge_provider(dataset: dict, provider_id: str, rows: list[dict]) -> dict:
    prov = next((p for p in dataset["providers"] if p["id"] == provider_id), None)
    if prov is None:
        return {"error": f"provider {provider_id} 不在数据集中"}
    existing = {m["id"]: m for m in prov["models"]}
    updated, added, skipped_retired = [], [], []
    for row in rows:
        mid = row["id"]
        if mid in existing:
            m = existing[mid]
            m["tiers"] = row["tiers"]
            if row.get("contextWindow"):
                m["contextWindow"] = row["contextWindow"]
            if row.get("status"):                    # 显式：仅非空字符串才覆盖
                m["status"] = row["status"]          # 官方标记 retired/limited 同步
            if row.get("notes"):                     # 显式：仅非空字符串才覆盖
                m["notes"] = row["notes"]
            m["fetchedAt"] = TODAY
            updated.append(mid)
        elif row.get("status") == "retired":
            skipped_retired.append(mid)               # 官方已下线的不入库
        else:
            row.setdefault("status", "active")
            row["fetchedAt"] = TODAY
            row.setdefault("officialUpdatedAt", None)
            row.setdefault("notes", "自动更新新增，请人工核对")
            prov["models"].append(row)
            added.append(mid)
    missing = [mid for mid in existing if mid not in {r["id"] for r in rows}]
    prov["checkedAt"] = TODAY
    return {"updated": updated, "added": added,
            "skippedRetired": skipped_retired, "missingInSource": missing}


# ---------------------------------------------------------------- 编排

def _validate_body(pid: str, body: bytes) -> bool:
    """节点胜出校验：200 之外还要能解析出价目行——地区限制/反爬页也是 200。"""
    parse = SOURCES[pid]["parse"]
    if parse is None:
        return True
    try:
        return bool(parse(body.decode("utf-8", "replace")))
    except Exception:
        return False


def update(providers: list[str] | None = None, max_nodes: int = 25,
           dry_run: bool = False, log=print, progress=None) -> dict:
    """progress(dict)：结构化进度回调（对齐 Go fetch.Progress），字段
    phase/item/itemIndex/itemTotal/done/total/pending/startedAt——前端据此渲染
    "在测哪个厂家/哪个节点/还剩多少/本项耗时"。无回调不影响行为。"""
    ilock = threading.Lock()  # inflight 被探测线程并发改，读取快照也要拿锁

    def emit(phase: str, **kw):
        if progress:
            with ilock:
                current = sorted(inflight)
            progress({"phase": phase,
                      "startedAt": int(time.time() * 1000),
                      "targets": targets,
                      "done": [t for t in targets if t in done_set],
                      "current": current, **kw})

    def bump(pid: str, on: bool):
        with ilock:
            if on:
                inflight.add(pid)
            else:
                inflight.discard(pid)

    targets = providers or list(SOURCES)
    dataset = json.load(open(MODELS_PATH, encoding="utf-8"))
    report: dict = {"fetchedAt": TODAY, "providers": {}}

    fetched: dict[str, str] = {}      # pid -> markdown text
    need_proxy: dict[str, str] = {}   # pid -> url
    done_set: set[str] = set()        # 已解决厂家（直连抓到/节点胜出）
    inflight: set[str] = set()        # 正在抓取的厂家（并发节点各抓一个，全亮）

    for i, pid in enumerate(targets):
        src = SOURCES[pid]
        bump(pid, True)
        emit("direct", item=pid, itemIndex=i + 1, itemTotal=len(targets))
        log(f"[{pid}] 直连抓取…")  # 「最近」行跟随当前项，不再停留在上一个厂家
        r = nodepool.fetch_url(src["url"], None, timeout=12)
        bump(pid, False)
        ok = r["status"] == 200 and r["bytes"] > MIN_USEFUL_BODY
        err = r.get("error") or r["status"]
        if ok and src["parse"] is not None:
            # 内联验空：墙页/反爬页立即转节点池，不等循环后统一判——
            # 否则 direct 阶段会把 anthropic 之类先标成成功。
            try:
                if not src["parse"](r["body"].decode("utf-8", "replace")):
                    ok, err = False, "直连返回内容解析为空（疑似地区限制/反爬页）"
            except Exception:
                pass  # 解析抛异常不算"空"——留给 merge 阶段报 parse-error
        if ok:
            fetched[pid] = r["body"].decode("utf-8", "replace")
            done_set.add(pid)
            emit("direct", item=f"✓ {pid} ← 直连")
            report["providers"][pid] = {"via": "direct", "status": 200}
            log(f"[{pid}] 直连成功 {r['bytes']}B")
        else:
            need_proxy[pid] = src["url"]
            report["providers"][pid] = {"via": "direct",
                                        "status": r["status"],
                                        "error": err}
            log(f"[{pid}] 直连失败 {err}，转节点池")

    if need_proxy:
        log(f"节点池探测 {len(need_proxy)} 个目标（上限 {max_nodes} 节点）…")
        def _onp(p: dict):
            if p.get("won"):  # 胜出瞬间：done 立刻变绿，不等汇总
                done_set.add(p["target"])
                emit(p.get("phase", "probe"),
                     item=f"✓ {p['target']} ← {p['node']}")
            elif "target" in p:  # 节点开抓/抓完某目标 → 在飞集合 + 组合进度
                bump(p["target"], p.get("inflight", True))
                emit(p.get("phase", "probe"), item=f"{p['node']} → {p['target']}",
                     itemIndex=p.get("idx"), itemTotal=p.get("total"))
            elif "node" in p:  # 节点开测事件
                emit(p.get("phase", "probe"), item=p["node"],
                     itemIndex=p.get("idx"), itemTotal=p.get("total"))
            else:  # 批次完成事件 → 日志
                log(f"  已测 {p['tested']} 节点，待解 {p['pending']}")

        probe = nodepool.find_working_proxy(
            need_proxy, max_nodes=max_nodes, validate=_validate_body,
            on_progress=_onp)
        for pid, win in probe["winners"].items():
            # 探测时已校验非空解析的响应体直接复用——重抓有内容抖动风险
            body = win.get("body") or b""
            if not body:
                try:
                    with nodepool.proxy_through_node(win["node_id"]) as lp:
                        body = nodepool.fetch_url(SOURCES[pid]["url"], lp.port,
                                                  timeout=15)["body"] or b""
                except Exception as e:
                    report["providers"][pid].update({"error": f"{type(e).__name__}: {e}"})
                    continue
            if len(body) > MIN_USEFUL_BODY:
                fetched[pid] = body.decode("utf-8", "replace")
                done_set.add(pid)
                report["providers"][pid] = {"via": f"node:{win['node']}",
                                            "status": 200}
                log(f"[{pid}] 经节点 {win['node']} 成功 {len(body)}B")
        report["unreachable"] = probe["pending"]

    changed = {}
    for pid, md in fetched.items():
        src = SOURCES[pid]
        if src["parse"] is None:
            report["providers"][pid]["action"] = "manual-check"
            log(f"[{pid}] 页面可达但无自动解析器 → 人工核对")
            continue
        try:
            rows = src["parse"](md)
        except Exception as e:
            report["providers"][pid].update({"action": "parse-error",
                                             "error": f"{type(e).__name__}: {e}"})
            continue
        if not rows:
            report["providers"][pid]["action"] = "parse-empty"
            continue
        res = merge_provider(dataset, pid, rows)
        changed[pid] = res
        report["providers"][pid].update({"action": "merged", **{k: len(v) for k, v in res.items()}})
        log(f"[{pid}] 合并：更新 {len(res['updated'])}，新增 {len(res['added'])}，"
            f"源中缺失 {len(res['missingInSource'])}")

    if changed and not dry_run:
        emit("merge")
        shutil.copy2(MODELS_PATH, MODELS_PATH + ".bak")
        with open(MODELS_PATH, "w", encoding="utf-8") as f:
            json.dump(dataset, f, ensure_ascii=False, indent=2)
        log(f"已写回 models.json（备份 models.json.bak）")
    elif dry_run:
        log("dry-run：不写回")
    report["changed"] = changed
    return report


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--providers", help="逗号分隔，默认全部")
    ap.add_argument("--max-nodes", type=int, default=25)
    ap.add_argument("--dry-run", action="store_true")
    a = ap.parse_args()
    pids = a.providers.split(",") if a.providers else None
    rep = update(pids, max_nodes=a.max_nodes, dry_run=a.dry_run)
    print(json.dumps(rep, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
