/* AI 价格归一化 — 纯计算模块（无 DOM，可被浏览器 <script> 与 node:test 复用）
 * 约定：
 * - 所有单价 = 每 1M tokens；tiers 按 upToTokens 升序，末档 null=无上限；
 *   命中第一个 upToTokens===null || promptTokens<=upToTokens 的档。
 * - 不换汇价 = 模型官方币种原值；换汇价 = 恒人民币（USD×rate，CNY 原样）。
 * - 分档五级开关：账户(profile) > 站 > 提供商 > 厂商 > 模型；上级关=全失档但不改子级状态。
 */
"use strict";

const CNY = "CNY";

// ---------- 基础 ----------

function pickTier(tiers, promptTokens) {
  for (const t of tiers) {
    if (t.upToTokens == null || promptTokens <= t.upToTokens) return t;
  }
  return tiers[tiers.length - 1];
}

function pickTierIndex(tiers, promptTokens) {
  for (let i = 0; i < tiers.length; i++) {
    if (tiers[i].upToTokens == null || promptTokens <= tiers[i].upToTokens) return i;
  }
  return tiers.length - 1;
}

function normalizeMix(mix) {
  const i = +mix.input || 0, c = +mix.cacheRead || 0, w = +mix.cacheWrite || 0, o = +mix.output || 0;
  const s = i + c + w + o;
  if (s <= 0) throw new Error("配比合计必须大于 0");
  return { input: i / s, cacheRead: c / s, cacheWrite: w / s, output: o / s };
}

function convert(amount, fromCurrency, rate) {
  // 换汇目标恒为 CNY：USD→×rate；CNY→原样
  if (amount == null) return null;
  return fromCurrency === CNY ? amount : amount * rate;
}

// ---------- 模型系列归类（规则驱动，不落库） ----------

const SERIES_RULES = [
  [/^gpt-6/, "gpt-6.x"], [/^gpt-5/, "gpt-5.x"], [/^gpt-4/, "gpt-4.x"],
  [/^o\d/, "o系"], [/^claude-fable/, "claude-fable"], [/^claude-mythos/, "claude-mythos"],
  [/^claude-opus/, "claude-opus"], [/^claude-sonnet/, "claude-sonnet"], [/^claude-haiku/, "claude-haiku"],
  [/^gemini-\d+\.\d+-pro/, "gemini-pro"], [/^gemini-\d+-pro/, "gemini-pro"],
  [/^gemini-.*flash-lite/, "gemini-flash-lite"], [/^gemini-.*flash/, "gemini-flash"],
  [/^gemini/, "gemini-其他"], [/^grok-4\.20/, "grok-4.20"], [/^grok-4/, "grok-4.x"], [/^grok/, "grok-其他"],
  [/^deepseek/, "deepseek"], [/^glm-5/, "glm-5.x"], [/^glm-4/, "glm-4.x"], [/^glm/, "glm-其他"],
  [/^kimi-k3/, "kimi-k3"], [/^kimi/, "kimi-其他"], [/^moonshot/, "kimi-其他"],
];

function classifySeries(modelId) {
  for (const [re, s] of SERIES_RULES) if (re.test(modelId)) return s;
  return "其他";
}

// ---------- 数据集索引 ----------

function modelIndex(dataset) {
  // -> Map<modelId, {model, provider(vendor)}>
  const idx = new Map();
  for (const p of dataset.providers || []) {
    for (const m of p.models || []) {
      if (!idx.has(m.id)) idx.set(m.id, { model: m, vendor: p });
    }
  }
  return idx;
}

function allModels(dataset) {
  const out = [];
  for (const p of dataset.providers || [])
    for (const m of p.models || []) out.push({ model: m, vendor: p });
  return out;
}

// ---------- 提供范围 ----------

/**
 * 范围=并集：(厂商×系列命中) ∪ (白名单成员)。
 * - 全不选 = 全量；只勾白名单 = 仅白名单；
 * - 厂商/系列 + 白名单 = 两者并集（如 openai 全部 + claude 几个模型）。
 */
function inScope(scope, model, vendorId) {
  if (!scope) return true;
  const wl = scope.modelIds || [];
  // 兼容旧结构 {vendorIds, series}：同一组系列作用于全部所选厂商
  let vendors = scope.vendors;
  if (!vendors && (scope.vendorIds || scope.series)) {
    vendors = {};
    for (const v of scope.vendorIds || []) vendors[v] = (scope.series || []).slice();
  }
  const hasVS = vendors && Object.keys(vendors).length;
  if (wl.includes(model.id)) return true;
  if (wl.length && !hasVS) return false;          // 纯白名单模式
  if (!hasVS) return true;                        // 全不选=全量
  const sels = vendors[vendorId];
  if (!sels) return false;
  return !sels.length || sels.includes(classifySeries(model.id));
}

// ---------- 价格解析 ----------

const tpOn = (obj) => !obj || !obj.tieredPricing || obj.tieredPricing.enabled !== false;

/**
 * 解析某「站/提供商」下某模型的实际单项价。
 * @param entry {model, vendor} 来自 modelIndex/allModels
 * @param ctx {profile, station, provider, promptTokens, offPeak}
 * @returns {input, output, cacheRead, discount, tierIndex, tierLabel, source, inScope}
 */
function resolveUnitPrices(entry, ctx) {
  const { model, vendor } = entry;
  const { profile, station, provider, promptTokens, offPeak } = ctx;

  // 站点范围为提供商范围的上界（交集）：两层都要包含
  if (!inScope(station && station.scope, model, vendor.id) ||
      !inScope(provider && provider.scope, model, vendor.id))
    return { inScope: false };

  // 覆盖查找：提供商级 > 档案全局级 > 官方
  const mOv = (provider.modelOverrides || []).find(o => o.modelId === model.id);
  const gOv = (profile.modelOverrides || []).find(o => o.modelId === model.id);
  const tiers = (mOv && mOv.basePrice && mOv.basePrice.tiers)
    || (gOv && gOv.basePrice && gOv.basePrice.tiers)
    || model.tiers;
  const source = mOv && mOv.basePrice ? "provider-custom"
    : gOv && gOv.basePrice ? "global-override" : "official";

  // 五级分档开关：账户→站→提供商→厂商→模型
  const vSw = (provider.vendorSwitches || []).find(v => v.vendorId === vendor.id);
  const tiered = tpOn(profile) && tpOn(station) && tpOn(provider) && tpOn(vSw) && tpOn(mOv);

  const ti = tiered ? pickTierIndex(tiers, promptTokens) : 0;
  const tier = tiers[ti];

  // 折扣链：模型覆盖 > 提供商默认 > 站默认 > 1
  const discount = (mOv && mOv.discount != null ? mOv.discount
    : provider.defaultDiscount != null ? provider.defaultDiscount
    : station.defaultDiscount != null ? station.defaultDiscount : 1);

  const peak = offPeak && model.offPeakRatio ? model.offPeakRatio : 1;
  const input = tier.input * peak;
  const output = tier.output * peak;
  const cacheRead = (tier.cacheRead != null ? tier.cacheRead : tier.input) * peak; // 无缓存价→按 input
  const cacheWrite5m = cacheWritePrice(tier, "5m", tier.input) * peak;
  const cacheWrite1h = cacheWritePrice(tier, "1h", null);
  return {
    inScope: true,
    input, output, cacheRead, cacheWrite5m,
    cacheWrite1h: cacheWrite1h == null ? null : cacheWrite1h * peak,
    discount, tierIndex: ti,
    tierLabel: tiers.length > 1 ? tierLabel(tier) : "—",
    source, tiered,
    noCachePrice: tier.cacheRead == null,
  };
}

function cacheWritePrice(tier, ttl, fallback) {
  const w = (tier.cacheWrites || []).find(x => x.ttl === ttl);
  if (w) return w.price;
  return fallback;
}

function tierLabel(tier) {
  return tier.upToTokens == null ? `>${fmtK(prevHint(tier))}` : `≤${fmtK(tier.upToTokens)}`;
}
function prevHint() { return ""; } // 占位：上限标签只画 ≤N / 顶档
function fmtK(n) {
  if (n == null) return "";
  return n >= 1000 ? `${Math.round(n / 1000)}K` : `${n}`;
}
// 更友好的档位标签：需要上一档上界；由调用方传 tiers+idx 时用 tierLabelOf
function tierLabelOf(tiers, idx) {
  const t = tiers[idx];
  if (t == null) return "—";
  if (t.upToTokens == null) {
    const prev = idx > 0 ? tiers[idx - 1].upToTokens : null;
    return prev != null ? `>${fmtK(prev)}` : "全部";
  }
  return `≤${fmtK(t.upToTokens)}`;
}

function blendedPrice(u, mix) {
  const m = normalizeMix(mix);
  return (u.input * m.input + u.cacheRead * m.cacheRead + u.cacheWrite5m * m.cacheWrite + u.output * m.output) * u.discount;
}

/** 账户整体公共预设默认值（未在任何层级配置时使用） */
const DEFAULT_MIX = { input: 4, cacheRead: 95, cacheWrite: 0, output: 1 };

/**
 * 社区经验配比（来源：TraceLab/UW 2026 编码智能体轨迹 ~95.7% 前缀命中；
 * GitHub Copilot 生产遥测 回合内90%/跨回合55%；Requesty 2026-10 客户端统计；
 * Arize 缓存基准 DeepSeek93.6/GLM84.3/GPT77.7）。为估算预设，非精确值。
 */
const COMMUNITY_VENDOR_MIX = {
  anthropic:   { input: 4,  cacheRead: 85, cacheWrite: 6,  output: 5  }, // Claude Code 轨迹：命中~85%且写占比高
  openai:      { input: 3,  cacheRead: 93, cacheWrite: 0,  output: 4  }, // Codex ~96%，自动缓存无显式写价
  google:      { input: 8,  cacheRead: 80, cacheWrite: 4,  output: 8  }, // 隐式缓存为主
  deepseek:    { input: 4,  cacheRead: 92, cacheWrite: 0,  output: 4  }, // 93.6% 命中，命中/未命中计价无独立写价
  zai:         { input: 10, cacheRead: 80, cacheWrite: 3,  output: 7  }, // GLM ~84%
  "bigmodel-cn": { input: 10, cacheRead: 80, cacheWrite: 3, output: 7 },
  moonshot:    { input: 12, cacheRead: 75, cacheWrite: 3,  output: 10 },
  xai:         { input: 15, cacheRead: 70, cacheWrite: 3,  output: 12 },
};

/** 场景原型预设（社区 workload 形态） */
const COMMUNITY_PRESETS = [
  { id: "cs-agentic",  name: "社区·智能体编程(均值)", promptTokens: 128000, mix: { input: 4,  cacheRead: 92, cacheWrite: 3,  output: 1  } },
  { id: "cs-claudecode", name: "社区·Claude轨迹",    promptTokens: 128000, mix: { input: 5,  cacheRead: 85, cacheWrite: 7,  output: 3  } },
  { id: "cs-chat",     name: "社区·对话交互",        promptTokens: 32000,  mix: { input: 55, cacheRead: 15, cacheWrite: 5,  output: 25 } },
  { id: "cs-longdoc",  name: "社区·长文写作/RAG",    promptTokens: 64000,  mix: { input: 75, cacheRead: 15, cacheWrite: 5,  output: 5  } },
  { id: "cs-noreuse",  name: "社区·无复用(翻译/摘要)", promptTokens: 16000, mix: { input: 85, cacheRead: 0,  cacheWrite: 0,  output: 15 } },
];

/**
 * 配比逐级解析，优先级：提供商 > 中转站 > 具体模型(覆盖/档案级模型预设)
 * > 厂商(档案级厂商预设) > 档案公共预设 > DEFAULT_MIX。
 * entry 用于查厂商/模型预设，可省略（此时跳过该两级）。
 */
function resolveMix(profile, station, provider, mOv, entry) {
  const mm = entry && (profile.modelMix || []).find(v => v.modelId === entry.model.id);
  const vm = entry && (profile.vendorMix || []).find(v => v.vendorId === entry.vendor.id);
  const cand = [provider && provider.mix, station && station.mix,
                mOv && mOv.mix, mm && mm.mix, vm && vm.mix,
                profile.scenario && profile.scenario.mix, DEFAULT_MIX];
  for (const m of cand) {
    if (m && ((+m.input || 0) + (+m.cacheRead || 0) + (+m.cacheWrite || 0) + (+m.output || 0)) > 0) return m;
  }
  return DEFAULT_MIX;
}

// ---------- 结果行生成 ----------

/**
 * 生成全档案结果明细行。
 * @returns [{profileId,profileName,stationId,stationName,providerId,providerName,
 *            vendorId,vendorName,series,modelId,modelName,tierLabel,discount,source,
 *            priceNative,currency,priceCny,officialCny}]
 */
function summaryRows(profiles, dataset, rate) {
  const idx = allModels(dataset);
  const rows = [];
  for (const pf of profiles || []) {
    const sc = pf.scenario || {};
    for (const st of pf.stations || []) {
      if (st.enabled === false) continue;
      for (const pr of st.providers || []) {
        if (pr.enabled === false) continue;
        for (const entry of idx) {
          const u = resolveUnitPrices(entry, {
            profile: pf, station: st, provider: pr,
            promptTokens: +sc.promptTokens || 0, offPeak: !!sc.offPeak,
          });
          if (!u.inScope) continue;
          const mOv = (pr.modelOverrides || []).find(o => o.modelId === entry.model.id);
          const rowMix = resolveMix(pf, st, pr, mOv, entry);
          const cur = entry.vendor.currency || "USD";
          const native = blendedPrice(u, rowMix);
          const ratio = +st.rechargeRatio || 1;   // ¥1 → N 代币
          // 官方基准价（无折扣、官方 tier、同档位、同配比；原币）
          const off = officialPrice(entry, pf, rate, rowMix);
          rows.push({
            profileId: pf.id, profileName: pf.name,
            stationId: st.id, stationName: st.name,
            providerId: pr.id, providerName: pr.name,
            vendorId: entry.vendor.id, vendorName: entry.vendor.name,
            series: classifySeries(entry.model.id),
            modelId: entry.model.id, modelName: entry.model.name || entry.model.id,
            tierLabel: tierLabelOf(
              (u.source === "official" ? entry.model.tiers
                : ((pr.modelOverrides || []).find(o => o.modelId === entry.model.id) || {}).basePrice?.tiers
                  || ((pf.modelOverrides || []).find(o => o.modelId === entry.model.id) || {}).basePrice?.tiers
                  || entry.model.tiers),
              u.tierIndex),
            tierIndex: u.tierIndex,
            discount: u.discount, source: u.source, tiered: u.tiered,
            priceNative: native, currency: cur,
            rechargeRatio: ratio, priceStation: native / ratio,
            priceCny: convert(native, cur, rate),
            officialNative: off,
            officialCny: convert(off, cur, rate),
            mixLabel: [rowMix.input, rowMix.cacheRead, rowMix.cacheWrite, rowMix.output]
              .map(x => +x || 0).join("|"),
          });
        }
      }
    }
  }
  return rows;
}

function officialPrice(entry, profile, rate, mix) {
  const sc = profile.scenario || {};
  const m = normalizeMix(mix || sc.mix || { input: 1, cacheRead: 0, output: 0 });
  const t = pickTier(entry.model.tiers, +sc.promptTokens || 0);
  const cr = t.cacheRead != null ? t.cacheRead : t.input;
  const cw = cacheWritePrice(t, "5m", t.input);
  const p = t.input * m.input + cr * m.cacheRead + cw * m.cacheWrite + t.output * m.output;
  return p;   // 原币价；需要 CNY 时由调用方 convert
}

// ---------- 单次估算 ----------

function estimateRequest(u, usage, opts) {
  // usage: {input, cacheRead, cacheWrite, output} 实际 tokens；档位已由调用方按 promptTokens 判好
  // opts.cacheTTL: '5m'(默认) | '1h'；模型无 1h 价时回退 5m 价
  const cw = opts && opts.cacheTTL === "1h" && u.cacheWrite1h != null
    ? u.cacheWrite1h : u.cacheWrite5m;
  return ((+usage.input || 0) * u.input
        + (+usage.cacheRead || 0) * u.cacheRead
        + (+usage.cacheWrite || 0) * cw
        + (+usage.output || 0) * u.output) / 1e6 * u.discount;
}

// ---------- 构造与克隆 ----------

let _seq = 0;
const uid = (p) => `${p}-${Date.now().toString(36)}-${(_seq++).toString(36)}`;

function newProvider(name, isDefault) {
  return {
    id: uid("pr"), name, isDefault: !!isDefault,
    enabled: true, defaultDiscount: null,
    tieredPricing: { enabled: true },
    scope: { vendorIds: [], series: [], modelIds: [] },
    vendorSwitches: [], modelOverrides: [],
  };
}

function newStation(name) {
  return {
    id: uid("st"), name, url: "",
    enabled: true, defaultDiscount: 1, rechargeRatio: 1, notes: "",
    tieredPricing: { enabled: true },
    scope: { vendorIds: [], series: [], modelIds: [] },  // 站点可提供范围（上界）
    providers: [newProvider(name, true)],   // 默认提供商与站同名
  };
}

function newProfile(name) {
  return {
    id: uid("pf"), name, notes: "",
    exchangeRate: { mode: "auto", manualValue: null,
                    auto: { value: null, source: null, fetchedAt: null } },
    scenario: {
      promptTokens: 128000,
      mix: { ...DEFAULT_MIX },
      offPeak: false, presets: COMMUNITY_PRESETS.map(p => ({ ...p, mix: { ...p.mix } })), activePreset: null,
    },
    tieredPricing: { enabled: true },
    stations: [], modelOverrides: [],
    vendorMix: Object.entries(COMMUNITY_VENDOR_MIX).map(([vendorId, mix]) => ({ vendorId, mix: { ...mix } })),
    modelMix: [],
    display: { precision: 4, targetCurrency: CNY },
  };
}

function defaultConfig() {
  const p = newProfile("默认配置");
  return { version: 2, activeProfileId: p.id, profiles: [p] };
}

/** F13 深拷贝节点并重新分配 id、名称加「副本」。kind: profile|station|provider|modelOverride */
function cloneNode(node, kind) {
  const c = JSON.parse(JSON.stringify(node));
  const reid = (o, prefix) => { o.id = uid(prefix); };
  if (kind === "profile") {
    reid(c, "pf"); c.name += " 副本";
    for (const st of c.stations || []) {
      reid(st, "st");
      for (const pr of st.providers || []) {
        reid(pr, "pr");
        (pr.modelOverrides || []).forEach(o => { o.id = uid("ov"); });
      }
    }
    (c.modelOverrides || []).forEach(o => { o.id = uid("ov"); });
  } else if (kind === "station") {
    reid(c, "st"); c.name += " 副本";
    for (const pr of c.providers || []) {
      reid(pr, "pr");
      (pr.modelOverrides || []).forEach(o => { o.id = uid("ov"); });
    }
  } else if (kind === "provider") {
    reid(c, "pr"); c.name += " 副本"; c.isDefault = false;
    (c.modelOverrides || []).forEach(o => { o.id = uid("ov"); });
  } else if (kind === "modelOverride") {
    reid(c, "ov");
  }
  return c;
}

/** 站改名时联动默认提供商（若仍与旧名相同） */
function renameStation(station, newName) {
  const old = station.name;
  station.name = newName;
  for (const pr of station.providers || []) {
    if (pr.isDefault && pr.name === old) pr.name = newName;
  }
}

// ---------- 分档编辑（操作符 → upToTokens 转换 + 校验） ----------

/**
 * rows: [{op:'<'|'≤'|'>'|'≥'|'all', value:number|null, input, cacheRead, output}]
 * 规则：非末档仅 </≤；末档可 >/≥（与上一档边界必须衔接）或 all（全部）；
 * 禁止重叠与缺漏。返回 {errors:[], tiers:[]}。
 */
function tiersFromEditor(rows) {
  const errors = [], tiers = [];
  if (!rows.length) return { errors: ["至少保留 1 档"], tiers };
  const px = (r) => ({
    input: +r.input || 0,
    output: +r.output || 0,
    cacheRead: r.cacheRead === "" || r.cacheRead == null ? null : +r.cacheRead,
  });
  rows.forEach((r, i) => {
    const last = i === rows.length - 1, n = i + 1;
    const v = +r.value;
    if (r.op !== "all" && !(v > 0)) { errors.push(`第${n}档：阈值须为正数`); return; }
    if (r.op === "all") {
      if (!last) errors.push(`第${n}档「全部」只能出现在最后一档，其后的档位不可达`);
      tiers.push({ upToTokens: null, ...px(r) });
    } else if (r.op === "<" || r.op === "≤") {
      tiers.push({ upToTokens: r.op === "<" ? v - 1 : v, ...px(r) });
      if (last) errors.push(`最后一档后存在缺漏：缺少 ${r.op === "<" ? "≥" : ">"}${v} 的档（或把本档改为「全部」）`);
    } else { // > 或 ≥
      if (!last) errors.push(`第${n}档「${r.op}」只能用于最后一档，否则后续档全部不可达`);
      if (i === 0) errors.push(`第${n}档：首档不能用「${r.op}」，低区间没有覆盖`);
      const bound = r.op === ">" ? v : v - 1;
      const prev = tiers[tiers.length - 1];
      if (prev && prev.upToTokens != null && prev.upToTokens !== bound) {
        errors.push(prev.upToTokens > bound
          ? `第${n}档与前一档重叠：前档上界 ${prev.upToTokens}，本档从 ${r.op}${v} 开始`
          : `第${n}档与前一档有缺漏：${prev.upToTokens} 到 ${bound} 之间没有覆盖`);
      }
      tiers.push({ upToTokens: null, ...px(r) });
    }
  });
  for (let i = 1; i < tiers.length; i++) {
    const a = tiers[i - 1].upToTokens, b = tiers[i].upToTokens;
    if (a != null && b != null && b <= a)
      errors.push(`第${i + 1}档上界 ${b} 必须大于前一档 ${a}`);
  }
  return { errors, tiers };
}

// ---------- 闲时/忙时 ----------

/** 返回 true=忙时 false=闲时 null=无官方定义/时间不可解析（UTC 工作日窗口，未含法定节假日） */
function isPeakTime(iso, model) {
  const w = model.peakWindows;
  if (!w || !w.length) return null;
  const d = new Date(iso);
  if (isNaN(d)) return null;
  const day = d.getUTCDay();
  if (day === 0 || day === 6) return false;
  const mins = d.getUTCHours() * 60 + d.getUTCMinutes();
  return w.some(([s, e]) => {
    const [sh, sm] = s.split(":").map(Number), [eh, em] = e.split(":").map(Number);
    return mins >= sh * 60 + sm && mins < eh * 60 + em;
  });
}

// ---------- CSV 对账 ----------

/** 简易 CSV 解析（支持引号与内嵌逗号/换行）。返回字符串数组的行。 */
function parseCsv(text) {
  const rows = []; let row = [], cur = "", q = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (q) { if (ch === '"') { if (text[i + 1] === '"') { cur += '"'; i++; } else q = false; } else cur += ch; }
    else if (ch === '"') q = true;
    else if (ch === ",") { row.push(cur); cur = ""; }
    else if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && text[i + 1] === "\n") i++;
      row.push(cur); rows.push(row); row = []; cur = "";
    } else cur += ch;
  }
  if (cur !== "" || row.length) { row.push(cur); rows.push(row); }
  return rows.filter(r => r.length > 1 || r[0] !== "");
}

const CSV_HEADERS = ["time", "station", "provider", "model", "input", "output", "cacheRead", "cacheWrite", "actual", "remark"];
const CSV_HEADER_CN = { "调用时间": "time", "中转站": "station", "服务商": "provider", "提供商": "provider", "模型": "model", "输入": "input", "输出": "output", "缓存读": "cacheRead", "缓存写": "cacheWrite", "实际扣费": "actual", "备注": "remark" };

/** csv 文本 → [{time,station,provider,model,input,output,cacheRead,cacheWrite,actual,remark}]（跳过表头） */
function parseBillCsv(text) {
  const rows = parseCsv(text);
  if (!rows.length) return [];
  let header = rows[0].map(c => c.trim());
  let start = 0;
  const mapped = header.map(c => CSV_HEADER_CN[c] || (CSV_HEADERS.includes(c) ? c : null));
  if (mapped.some(Boolean)) { header = mapped; start = 1; }
  else header = CSV_HEADERS.slice(0, header.length); // 无表头按固定列序
  return rows.slice(start).map(r => {
    const o = {};
    header.forEach((k, i) => { if (k) o[k] = (r[i] || "").trim(); });
    for (const k of ["input", "output", "cacheRead", "cacheWrite", "actual"]) o[k] = +o[k] || 0;
    return o;
  });
}

/**
 * 逐行核对理论扣费 vs 实际扣费。
 * 备注语义：有闲忙时区分的模型——非空=忙时、空=闲时；Claude 系（有 cacheWrite 双价）——非空=1h 缓存写。
 * 时间校验：模型带 peakWindows 时按 UTC 判定闲忙，与备注冲突 → warn。
 * @returns [{rec,status,flags[],theory,actual,diff,currency}]
 */
function reconcile(records, profile, dataset) {
  const idx = modelIndex(dataset);
  return records.map(rec => {
    const st = (profile.stations || []).find(s => s.name === rec.station);
    const pr = st && (st.providers || []).find(p => p.name === rec.provider);
    const entry = idx.get(rec.model);
    const out = { rec, status: "ok", flags: [], theory: null, actual: rec.actual, currency: entry ? entry.vendor.currency : "USD" };
    if (!st) { out.status = `站「${rec.station}」不存在`; return out; }
    if (!pr) { out.status = `提供商「${rec.provider}」不存在`; return out; }
    if (!entry) { out.status = `模型「${rec.model}」不在价目`; return out; }
    const remark = !!(rec.remark && String(rec.remark).trim());
    const hasOff = !!entry.model.offPeakRatio;
    const hasDualCW = entry.model.tiers.some(t => (t.cacheWrites || []).length > 1);
    // 闲忙：备注非空=忙时
    const offPeak = hasOff ? !remark : false;
    if (hasOff) {
      const pk = isPeakTime(rec.time, entry.model);
      if (pk !== null && pk !== remark)
        out.flags.push(pk ? "调用时间在官方忙时，备注却记为闲时" : "调用时间在官方闲时，备注却记为忙时");
    }
    // Claude 缓存写 TTL：备注非空=1h
    const cacheTTL = hasDualCW && remark ? "1h" : "5m";
    const u = resolveUnitPrices(entry, {
      profile, station: st, provider: pr,
      promptTokens: rec.input + rec.cacheRead + rec.cacheWrite,
      offPeak,
    });
    if (!u.inScope) { out.status = "模型不在提供商范围"; return out; }
    out.theory = estimateRequest(u, rec, { cacheTTL });
    out.diff = out.theory - rec.actual;
    out.u = u;
    return out;
  });
}

// ---------- 校验 ----------

function validateConfig(json) {
  const errors = [];
  const req = (cond, msg) => { if (!cond) errors.push(msg); };
  req(json && typeof json === "object", "配置必须是 JSON 对象");
  if (!json || typeof json !== "object") return { ok: false, errors };
  req(Array.isArray(json.profiles), "profiles 必须是数组");
  for (const pf of json.profiles || []) {
    req(pf.id && pf.name, "档案缺少 id/name");
    const sc = pf.scenario || {};
    if (sc.mix) {
      const s = (+sc.mix.input || 0) + (+sc.mix.cacheRead || 0) + (+sc.mix.output || 0);
      req(s > 0, `档案「${pf.name}」配比合计必须>0`);
    }
    for (const st of pf.stations || []) {
      req(Array.isArray(st.providers) && st.providers.length >= 1,
          `站点「${st.name}」必须至少有 1 个提供商`);
      for (const pr of st.providers || []) {
        for (const o of pr.modelOverrides || []) {
          if (o.discount != null) req(o.discount > 0 && o.discount <= 10,
              `覆盖「${o.modelId}」折扣须在 (0,10]`);
          if (o.basePrice) validateTiers(o.basePrice.tiers, o.modelId, errors);
        }
      }
    }
    for (const o of pf.modelOverrides || []) {
      if (o.basePrice) validateTiers(o.basePrice.tiers, o.modelId, errors);
    }
  }
  return { ok: errors.length === 0, errors };
}

function validateTiers(tiers, modelId, errors) {
  const req = (cond, msg) => { if (!cond) errors.push(msg); };
  req(Array.isArray(tiers) && tiers.length >= 1, `「${modelId}」tiers 至少 1 档`);
  let prev = -1;
  (tiers || []).forEach((t, i) => {
    req(typeof t.input === "number" && t.input >= 0, `「${modelId}」档${i} input 须 ≥0`);
    req(typeof t.output === "number" && t.output >= 0, `「${modelId}」档${i} output 须 ≥0`);
    if (t.upToTokens != null) {
      req(t.upToTokens > prev, `「${modelId}」档${i} upToTokens 须升序`);
      prev = t.upToTokens;
    }
    if (i === tiers.length - 1) req(t.upToTokens == null, `「${modelId}」末档须无上限`);
    else req(t.upToTokens != null, `「${modelId}」非末档须有上限`);
  });
}

// ---------- 导出 ----------

const Calc = {
  CNY, pickTier, pickTierIndex, normalizeMix, convert,
  classifySeries, modelIndex, allModels, inScope,
  resolveUnitPrices, resolveMix, blendedPrice, summaryRows, officialPrice,
  estimateRequest, newProvider, newStation, newProfile, defaultConfig,
  cloneNode, renameStation, validateConfig, validateTiers, tierLabelOf, fmtK,
  tiersFromEditor, isPeakTime, parseCsv, parseBillCsv, reconcile, cacheWritePrice, DEFAULT_MIX,
  COMMUNITY_VENDOR_MIX, COMMUNITY_PRESETS,
};

if (typeof module !== "undefined" && module.exports) module.exports = Calc;
if (typeof window !== "undefined") window.Calc = Calc;
