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
│ server/index.js   Node 零依赖 http 服务    │
│  ├─ 静态文件 public/                      │
│  ├─ GET/PUT /api/config  → data/config.json│
│  ├─ GET /api/models      → data/models.json│
│  └─ GET /api/exchange-rate → 上游代理+缓存 │
└──────────────────────────────────────────┘
```

设计取舍：

- **零依赖**：无框架、无构建、无第三方包；`node server/index.js` 即启动。代价是手写约百行路由/静态服务，换取零供应链与极简部署。
- **calc.js 独立**：归一化/档位/折扣/折算全部纯函数放在前端模块中，同时被 `index.html`（浏览器 `<script src>`）与 `tests/`（`node:test` import）使用 —— 计算逻辑单一真相且可无 DOM 测试。
- **后端是可选增强**：前端所有计算在本地完成；后端仅负责配置持久化与汇率代理。后端不可达 → localStorage 降级 + 手动汇率兜底，功能完整。

## 2. 目录结构

```
ai-price-calculate/
├─ public/
│  ├─ index.html      # 单文件 SPA（结构+样式+视图逻辑，内联）
│  └─ calc.js         # 纯函数：tier 命中、折扣合并、归一化、换汇、校验
├─ server/
│  └─ index.js        # http 服务：静态 + REST API + 汇率代理（零依赖）
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

### 3.2 `data/config.json`（用户配置）

```jsonc
{
  "version": 1,
  "exchangeRate": {
    "mode": "auto",                    // auto | manual
    "manualValue": 7.10,
    "auto": { "value": null, "source": null, "fetchedAt": null }
  },
  "scenario": {
    "promptTokens": 128000,
    "mix": { "input": 0.7, "cacheRead": 0.2, "output": 0.1 },  // 存原始输入，计算时归一化
    "offPeak": false,                  // 全局开关：对带 offPeakRatio 的模型生效
    "presets": [{ "id": "p1", "name": "编码 3:1", "promptTokens": 128000, "mix": {...} }],
    "activePreset": null
  },
  "stations": [{
    "id": "st-…", "name": "示例站 A", "url": "",
    "currency": "USD",                  // 该站计费币种；决定自定义基础价与“不换汇”显示币种
    "enabled": true, "defaultDiscount": 0.8, "notes": "",
    "overrides": [{
      "modelId": "gpt-6.2-astra",
      "discount": 0.6,                  // 可选；缺省→站默认
      "basePrice": {                    // 可选；完全自定义（含自定义分档），币种=station.currency
        "tiers": [{ "upToTokens": 272000, "input": 5, "output": 25, "cacheRead": 0.25 },
                  { "upToTokens": null, "input": 10, "output": 37.5, "cacheRead": 0.5 }]
      }
    }]
  }],
  "modelOverrides": [{                  // 全局官方价修正（最低优先级）
    "modelId": "…", "basePrice": { "tiers": [...] }
  }],
  "display": { "precision": 4, "targetCurrency": "CNY" }
}
```

优先级（高→低）：`station.overrides[].basePrice` > `modelOverrides[].basePrice` > 官方 `models.json`；折扣同理（模型覆盖折扣 > 站默认 > 1）。

### 3.3 计算契约（calc.js 导出）

```js
pickTier(tiers, promptTokens) -> tier                 // 见 3.1 语义
resolveUnitPrices(model, station?, globalOv?, promptTokens, offPeak) -> {input, output, cacheRead, discount, tierIndex, source: 'station-custom'|'station-discount'|'global-override'|'official'}
normalizeMix(mix) -> {input, cacheRead, output}       // 和=1；全 0 → 报错
blendedPrice(unitPrices, mix) -> number               // 本币每 1M 混合 tokens
convert(amount, fromCurrency, toCurrency, rate) -> number
estimateRequest(unitPrices, usage, discount) -> number // F8 单次请求
validateConfig(json) -> {ok, errors[]}                 // F10 导入校验
```

## 4. API 契约（server/index.js）

| 方法/路径 | 行为 | 说明 |
|---|---|---|
| `GET /` `GET /<file>` | 服务 `public/` 静态文件 | index.html 默认；防目录穿越 |
| `GET /api/models` | 返回 models.json | 加 `checkedAt` 头信息即可 |
| `GET /api/config` | 返回 config.json；不存在→返回默认配置 | 启动时若无则落默认 |
| `PUT /api/config` | 校验后原子写 config.json | `validateConfig` 服务端再验一次；临时文件+rename |
| `GET /api/exchange-rate` | 返回 `{rate, source, fetchedAt, stale}` | 缓存<24h 直接回；否则请求 `open.er-api.com/v6/latest/USD`→取 `rates.CNY`，失败回退 `api.frankfurter.dev/v1/latest?base=USD&symbols=CNY`，再失败回缓存+`stale:true` |
| `POST /api/config/import` | 同 PUT 但语义为导入（返回 errors） | 可合并入 PUT |

错误格式：`{ "error": "code", "message": "…", "details": [...] }`，4xx 校验 / 5xx 存储与上游。

## 5. 汇率策略

- 主源 `open.er-api.com`（免 key、每日更新、需署名：UI 页脚加 `Rates by ExchangeRate-API` 链接）；备源 Frankfurter（ECB、CORS 开放、无需署名）。
- 服务端文件缓存 `data/exchange-rate-cache.json`（`{rate, source, fetchedAt}`），24h 内不重复请求上游；`manual` 模式不发起任何请求。
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

- 开发：`node server/index.js` → http://127.0.0.1:8787（端口 env `PORT` 可改）。
- 无构建产物；git clone + node 即可跑；亦可纯前端打开 `public/index.html`（file:// 下降级 localStorage，汇率自动模式不可用需手动）。

## 9. 演化方向（v2+ 预留）

- 站内分组（一个站点多个分组倍率）；输出长度子档（bigmodel.cn 口径）；缓存写入纳入配比；价目自动更新脚本（models.json refresh）；多场景并排对比视图。
