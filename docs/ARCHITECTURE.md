# ARCHITECTURE — AI 中转站价格归一化计算器

版本：v0.1（待批准） · 日期：2026-11-24

## 1. 总览

```
┌─ 浏览器 ─────────────────────────────────┐
│ public/index.html  单文件 SPA（视图+交互） │
│ public/calc.js     纯计算模块（可单测）     │
└───────┬──────────────────────────────────┘
        │ fetch（失败降级 localStorage）
┌───────▼──────────────────────────────────┐
│ server/app.py    Python 零依赖 http 服务   │
│  ├─ 静态文件 public/                      │
│  ├─ GET/PUT /api/config  → data/config.json│
│  ├─ GET /api/models      → data/models.json│
│  ├─ GET /api/fx          → 汇率代理+缓存   │
│  ├─ GET /api/nodes       → v2rayN 节点清单 │
│  └─ POST /api/pricing/update → 官方价目更新│
│       （nodepool.py 读 v2rayN 节点起独立    │
│         xray/sing-box 端口逐个探测可用出口） │
└──────────────────────────────────────────┘
```

设计取舍：

- **零依赖**：前后端均无第三方包；后端用 Python 3 标准库（`http.server`+`urllib`，原生支持代理），`python -m server.app` 即启动。换取零供应链与极简部署。
- **calc.js 独立**：归一化/档位/折扣/折算全部纯函数放在前端模块中，同时被 `index.html`（浏览器 `<script src>`）与 `tests/`（`node:test` import）使用 —— 计算逻辑单一真相且可无 DOM 测试。
- **后端是可选增强**：前端所有计算在本地完成；后端负责配置持久化、汇率代理与官方价目抓取。后端不可达 → localStorage 降级 + 手动汇率兜底，功能完整。
- **官方价目抓取**（`server/fetcher.py`）：只对接厂商官方 `.md` 文档页，逐厂商适配器解析价目表；直连失败时经 `server/nodepool.py` 遍历 v2rayN 节点库（VLESS+Reality→HY2，独立拉起 xray/sing-box 本地端口，不改用户 v2rayN 配置）寻找可用出口。抓不到的厂商输出人工核对清单，不编造价格。合并只动 `models.json` 厂商价目，永不触碰 `config.json` 用户配置。

## 2. 目录结构

```
ai-price-calculate/
├─ public/
│  ├─ index.html      # 单文件 SPA（结构+样式+视图逻辑，内联）
│  └─ calc.js         # 纯函数：tier 命中、折扣合并、归一化、换汇、校验
├─ server/
│  ├─ app.py          # http 服务：静态 + REST API + 汇率代理（零依赖）
│  ├─ nodepool.py     # v2rayN 节点库读取→xray/sing-box 独立端口→目标探测
│  └─ fetcher.py      # 官方价目抓取/解析/合并（CLI：python -m server.fetcher）
├─ data/
│  ├─ models.json     # 官方价目（入库、版本化、checkedAt）
│  ├─ config.json     # 运行时用户配置（.gitignore，原子写入）
│  └─ exchange-rate-cache.json  # 汇率缓存（.gitignore）
├─ tests/
│  ├─ calc.test.js    # 归一化/档位/优先级/折算（node:test）
│  └─ api.test.js     # config 读写、导入导出校验（node:test）
├─ design/wireframes/ # A 阶段线框产物
├─ docs/              # 本文档等
├─ package.json       # 仅 scripts 与元数据（无 dependencies）
└─ AGENTS.md
```

## 3. 数据模型

### 3.1 `data/models.json`（官方价目，已实现）

```jsonc
{
  "meta": { "version": "2026-11-24", "tierBasis": "...", "fields": {...} },
  "providers": [{
    "id": "anthropic", "name": "Anthropic",
    "currency": "USD",                  // USD | CNY
    "source": "https://...", "checkedAt": "2026-11-24",
    "models": [{
      "id": "claude-haiku-5-5", "name": "Claude Haiku 5.5",
      "contextWindow": null,            // tokens 或 null=未知
      "status": "active",               // active|deprecated|retired|preview|limited
      "offPeakRatio": 0.5,              // 可选：非高峰系数（DeepSeek）
      "tiers": [{                        // 按 upToTokens 升序；末档 null=无上限
        "upToTokens": 100000,
        "input": 0.10, "output": 0.50, "cacheRead": 0.01,
        "cacheWrites": [{ "ttl": "5m", "price": 0.125 }]   // 可选，展示用
      }, { "upToTokens": null, "input": 0.50, "output": 2.50, "cacheRead": 0.05 }]
    }]
  }]
}
```

档位语义：以 `promptTokens`（未命中输入+缓存读合计）顺序命中第一个 `upToTokens===null || promptTokens<=upToTokens` 的档；整档价作用于全部 tokens（非分段累进），与官方口径一致。

### 3.2 `data/config.json`（用户配置，v2）

顶层是多份**配置档案**（profile），每份独立保存一套「场景 + 汇率 + 站点树」，互不覆盖：

```jsonc
{
  "version": 2,
  "activeProfileId": "pf-…",
  "profiles": [{
    "id": "pf-…", "name": "主力测算", "notes": "",
    "exchangeRate": { "mode": "auto", "manualValue": 7.10,
                      "auto": { "value": null, "source": null, "fetchedAt": null } },
    "scenario": {
      "promptTokens": 128000,
      "mix": { "input": 0.7, "cacheRead": 0.2, "output": 0.1 },  // 存原始输入，计算时归一化
      "offPeak": false,
      "presets": [{ "id": "p1", "name": "编码 3:1", "promptTokens": 128000, "mix": {...} }],
      "activePreset": null
    },
    "tieredPricing": { "enabled": true },     // ① 账户级分档开关
    "stations": [{
      "id": "st-…", "name": "示例站 A", "url": "",
      "currency": "USD",                      // 该站计费币种
      "enabled": true, "defaultDiscount": 0.8, "notes": "",
      "tieredPricing": { "enabled": true },   // ② 站级
      "providers": [{                         // ≥1 个；建站自动生成与站同名的默认提供商
        "id": "pr-…", "name": "示例站 A",     //   默认提供商：isDefault=true，改站名联动
        "isDefault": true,                    //   站内最后一个提供商不可删除，只可禁用
        "enabled": true, "defaultDiscount": null,   // null→继承站默认折扣
        "tieredPricing": { "enabled": true }, // ③ 提供商级
        "scope": {                            // 提供范围：外=不产价
          "vendorIds": ["anthropic", "google"],   // 空数组=全部厂商
          "series": [],                            // 空=全部系列（如 claude-opus, gpt-6.x）
          "modelIds": []                           // 空=按上两级；非空=白名单精确指定
        },
        "vendorSwitches": [{                  // ④ 厂商级分档开关（该提供商内某厂商）
          "vendorId": "google", "tieredPricing": { "enabled": true }
        }],
        "modelOverrides": [{                  // ⑤ 模型级（折扣/自定价/分档开关）
          "modelId": "gpt-6.2-astra",
          "discount": 0.6,
          "tieredPricing": { "enabled": true },
          "basePrice": { "tiers": [{ "upToTokens": 272000, "input": 5, "output": 25, "cacheRead": 0.25 },
                                   { "upToTokens": null, "input": 10, "output": 37.5, "cacheRead": 0.5 }] }
        }]
      }]
    }],
    "modelOverrides": [{                      // 全局官方价修正（最低优先级）
      "modelId": "…", "basePrice": { "tiers": [...] }
    }],
    "display": { "precision": 4, "targetCurrency": "CNY" }
  }]
}
```

优先级（高→低）：`provider.modelOverrides[].basePrice` > `profile.modelOverrides[].basePrice` > 官方 `models.json`；折扣：`provider.modelOverrides[].discount` > `provider.defaultDiscount` > `station.defaultDiscount` > 1。

分档计价开关五级（账户 → 站 → 提供商 → 厂商 → 模型）：任一上级 `enabled=false` 时作用域内全部分档失效（按 `tiers[0]` 基础档计），但**不改写子级开关状态**——上级重开后原样恢复。

提供商范围 `scope`：`modelIds` 非空→白名单精确命中；否则按 `vendorIds`∩`series`（均空=全量）。范围外模型在该提供商的结果列/汇总行显示「—」。

层级复制（F13）：`cloneNode(profile|station|provider|modelOverride)` 深拷贝子树、重新分配所有 id、名称加「副本」后缀；档案复制额外置 `activeProfileId` 不变。

模型系列（series）由 `calc.js` 按规则归类（不落库）：取 modelId 主版本前缀——`gpt-6.x`/`gpt-5.x`/`gpt-4.x`/`o系`、`claude-(fable|mythos|opus|sonnet|haiku)-X`、`gemini-N.x-(pro|flash|flash-lite)`、`grok-4.x`、`deepseek-*`、`glm-N.x*`、`kimi-kN`；正则表驱动，未匹配归「其他」。

### 3.3 计算契约（calc.js 导出）

```js
pickTier(tiers, promptTokens) -> tier                 // 见 3.1 语义
inScope(provider.scope, model) -> bool                // modelIds 白名单 > vendorIds∩series
classifySeries(modelId) -> string                     // 规则归类（3.2 末）
resolveUnitPrices(model, station, provider, globalOv, scenario, profile) ->
    {input, output, cacheRead, discount, tierIndex,
     source: 'provider-custom'|'provider-discount'|'global-override'|'official',
     tiered: bool}                                    // 五级开关与运算结果
normalizeMix(mix) -> {input, cacheRead, output}       // 和=1；全 0 → 报错
blendedPrice(unitPrices, mix) -> number               // 本币每 1M 混合 tokens
convert(amount, fromCurrency, toCurrency, rate) -> number
estimateRequest(unitPrices, usage, discount) -> number // F8 单次请求
cloneNode(node, kind) -> node                         // F13 深拷贝+新 id+「副本」名
summaryRows(profiles, models, rate) -> [{             // F14 汇总页明细行
    profileId, profileName, stationId, stationName, providerId, providerName,
    vendorId, series, modelId, tierIndex,
    priceNative, currency, priceConverted, targetCurrency }]
validateConfig(json) -> {ok, errors[]}                 // F10 导入校验（含 providers≥1 约束）
```

## 4. API 契约（server/app.py，已实现）

| 方法/路径 | 行为 | 说明 |
|---|---|---|
| `GET /` `GET /<file>` | 服务 `public/` 静态文件 | index.html 默认；防目录穿越 |
| `GET /api/models` | 返回 models.json | — |
| `GET /api/config` | 返回 config.json；不存在→返回默认配置 | — |
| `PUT /api/config` | 校验后写 config.json | 原子化待补 |
| `GET /api/fx` | `{rate, source, fetchedAt, cached}` | `data/fx-cache.json` 24h 缓存 |
| `GET /api/nodes` | v2rayN 候选节点清单（脱敏：id/名称/类型/延迟） | 不含任何凭据字段 |
| `POST /api/pricing/update` | 后台跑 `fetcher.update` → `{jobId}` | body: `{providers?, maxNodes?}` |
| `GET /api/jobs/<id>` | `{status: running|done|error, report, log}` | 内存任务表 |

价目更新合并规则：仅更新厂商 `tiers`/`contextWindow`/`status`/`fetchedAt`/`checkedAt`；源中新增模型以 `notes:"自动更新新增，请人工核对"` 入库；官方标记 `retired` 的不入库；源中消失的数据集模型只报告 `missingInSource` 不自动删除；写回前备份 `models.json.bak`。`config.json` 永不被更新流程修改。

错误格式：`{ "error": "code", "message": "…", "details": [...] }`，4xx 校验 / 5xx 存储与上游。

## 5. 汇率策略

- 主源 `open.er-api.com`（免 key、每日更新、需署名：UI 页脚加 `Rates by ExchangeRate-API` 链接）；备源 Frankfurter（ECB、CORS 开放、无需署名）。
- 服务端文件缓存 `data/fx-cache.json`（`{rate, source, fetchedAt}`），24h 内不重复请求上游；`manual` 模式不发起任何请求。
- 前端在 auto 模式启动时调用一次；手动「刷新」按钮强制 `?refresh=1` 绕过缓存。

## 6. 前端结构（index.html 内部）

- 无框架：原生 DOM + 少量渲染辅助（`h()`/`render()`）；状态单一对象 `state = {models, config, rate, ui}`，全部修改走 `setState→render→persist(debounced PUT + localStorage)`。
- 页签/分区：场景栏（顶部常驻）、结果表、中转站管理、官方价目。
- 样式：内联 `<style>`，桌面优先、响应式及格；表格横向滚动。
- 国际化：界面中文（用户语言环境）；代码标识符英文。

## 7. 安全与健壮性

- 本地工具假设受信用户；不做鉴权（绑定 `127.0.0.1` 默认，文档说明如需局域网访问自行放开）。
- 输入全走 `validateConfig` 白名单校验（类型、范围、tiers 升序、id 白名单）；不执行任何用户 JSON 中的代码；渲染一律 `textContent`（无 innerHTML 注入面）。
- 上游请求 5s 超时；写文件原子操作；不记录敏感信息（配置中无密钥字段，文档明确不存 API Key）。

## 8. 部署/运行

- 开发：`python -m server.app --port 8787`（或 `npm start`）→ http://127.0.0.1:8787。
- 价目更新：`python -m server.fetcher [--providers xai,zai] [--max-nodes N] [--dry-run]`（或 `npm run update-pricing`）；节点探测单独跑：`python -m server.nodepool [N]`。
- 无构建产物；git clone + python 即可跑后端；前端亦可直接打开 `public/index.html`（file:// 下降级 localStorage，汇率自动模式不可用需手动）。
- 前置条件：价目更新的节点池功能需要本机已安装 v2rayN（读 `%LOCALAPPDATA%/v2rayN` 的只读节点库与 bin/ 下 xray、sing-box）；无 v2rayN 时仍可直连抓取与人工核对。

## 9. 演化方向（v2+ 预留）

- 站内分组（一个站点多个分组倍率）；输出长度子档（bigmodel.cn 复合口径精确选档）；缓存写入纳入配比；OpenAI Batch/Flex/Fast/Ultrafast 多价目表与 Anthropic/Google Batch 价纳入计算维度；Kimi 页面 JS 渲染的浏览器自动化解析；多场景并排对比视图。
