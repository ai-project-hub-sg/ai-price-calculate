# TEST_PLAN — 测试策略与用例

范围裁剪：单用户本地工具，无鉴权/并发；性能测试从简（数据量 <100 模型 × <20 站，纯前端计算非瓶颈）。重点是**计算正确性**与**配置健壮性**。

## 1. 单元测试 `tests/calc.test.js`（node:test，无 DOM）

| 用例 | 输入 | 期望 |
|---|---|---|
| 档位命中-低档 | Haiku5.5 tiers, promptTokens=80000 | 命中 upTo=100000 档（$0.10/$0.50） |
| 档位命中-跨档 | 同上, promptTokens=200000 | 命中末档（$0.50/$2.50），tierIndex=1 |
| 档位边界 | promptTokens=100000（含阈值） | 仍低档（`<=` 语义） |
| 归一化配比 | mix {7,2,1} vs {0.7,0.2,0.1} | 结果相等；和=1 |
| 配比全零 | mix {0,0,0} | normalizeMix 抛错/返回错误对象 |
| 缺缓存价回退 | cacheRead=null 模型, mix.cacheRead>0 | 缓存读按 input 价计 |
| 折扣优先级 | 模型覆盖 0.6 vs 站默认 0.8 | 用 0.6；无覆盖用 0.8；站无折扣用 1 |
| 基础价优先级 | 站自定 basePrice(分档) + 全局 override + 官方 | 依次取最高优先；自定档也按 promptTokens 命中 |
| offPeak | DeepSeek 模型 offPeak=true | 结果 ×0.5；无 offPeakRatio 的模型不受影响 |
| 换汇双向 | 100 USD↔CNY, rate=7.2 | 720；CNY→USD 用除法且目标币种=来源时原样返回 |
| 单次估算 | usage 三项 × 单项价 | 与手算一致（含 discount） |
| tiers 校验 | 乱序/末档非 null/重复阈值 | validateConfig 报错 |

## 2. API 测试 `tests/api.test.js`

| 用例 | 期望 |
|---|---|
| GET /api/config（无文件） | 返回默认配置结构 |
| PUT /api/config 合法 | 200 且文件原子落盘；GET 回读一致 |
| PUT 非法（坏类型/负价） | 400 + errors 明细，文件不被破坏 |
| GET /api/models | 返回 providers 数组且 meta.version 存在 |
| GET /api/exchange-rate | 无网/打桩上游：返回缓存或 stale:true；手动模式不发请求 |
| 静态服务 | `/` 返回 index.html；`/../x` 目录穿越被拒 |

测试方式：`node:test` 内起临时端口实例 + fetch 打桩上游（拦截全局 fetch 或注入 fetcher）。

## 3. 手工冒烟 / E2E 清单（Demo 与验收门执行并记录）

| # | 步骤 | 期望 |
|---|---|---|
| S1 | `npm start` 打开首页 | 场景栏+空态引导可见，无控制台报错 |
| S2 | 新增示例站（0.8 折） | 结果表出现该站列，数值=官方×0.8 |
| S3 | 站内覆盖模型折扣 0.5 | 该模型行单独变价，其余不变 |
| S4 | 站内自定义基础价含分档 | 改 promptTokens 跨档时该格按自定义档跳变 |
| S5 | 配比 70/20/10 → 70/30/0 | 缓存读相关项下降、排序变化正确 |
| S6 | 汇率手动 7.0 → 自动获取 | 显示来源/时间；关网刷新→stale 提示不白屏 |
| S7 | 刷新页面 | 配置全保留（后端模式） |
| S8 | 杀后端重开页面 | 降级横幅 + localStorage 配置仍在 |
| S9 | 导出→清配置→导入 | 完整还原；手改 JSON 破坏字段→逐条报错 |
| S10 | file:// 双击 index.html | 可算价（自动汇率不可用→手动），降级正常 |

## 4. 安全/边界

- 渲染注入：站点名输入 `<img onerror>` → 显示为纯文本（textContent）。
- 超大数值（promptTokens=1e18、价格 1e9）：不崩溃，格式化显示。
- 非法 tiers（末档有限、阈值 0/负）在表单与导入双侧被拒。

## 5. 发布前冒烟（每次交付执行）

`node --check`（全部 js）→ `node --test tests/` → S1-S3 → 记录结果于 PR/验收记录。
