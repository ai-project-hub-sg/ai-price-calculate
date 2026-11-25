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
| 提供商范围 | scope={vendorIds:[anthropic]} 对 grok 模型 | inScope=false → 该列「—」；modelIds 白名单优先 |
| 系列归类 | gpt-6.2-astra/claude-opus-5-5/glm-5.3 | → gpt-6.x/claude-opus/glm-5.x |
| 分档五级开关 | 账户关→全树失档；提供商关→仅该商失档 | 上级关不改子级字段；重开后状态原样恢复 |
| 折扣链 | 模型0.6 > 提供商默认0.7 > 站默认0.8 > 1 | 逐级取最高优先 |
| 层级复制 | cloneNode(provider) 改副本折扣 | 原提供商数据不变（深拷贝+新 id） |
| 汇总行生成 | 2 档案×2 站×含范围外 | 行数=启用且在范围内的组合；字段齐全 |
| 汇总排序/筛选 | 按换汇列排序 + 厂商=anthropic+系列=opus | 排序正确且仅留匹配行 |
| 默认提供商 | 建站 → providers[0].name===站名,isDefault | 改站名联动；单独改名后不再联动 |

## 2. API 测试 `tests/api.test.js`

| 用例 | 期望 |
|---|---|
| GET /api/config（无文件） | 返回默认配置结构 |
| PUT /api/config 合法 | 200 且文件原子落盘；GET 回读一致 |
| PUT 非法（坏类型/负价） | 400 + errors 明细，文件不被破坏 |
| GET /api/models | 返回 providers 数组且 meta.version 存在 |
| GET /api/fx | 无网/打桩上游：返回缓存或报错；24h 内缓存命中 |
| GET /api/nodes | 返回候选节点清单且无任何凭据字段（仅 id/name/kind/delay） |
| POST /api/pricing/update + GET /api/jobs | 返回 jobId→轮询 done；报告含 updated/added/missingInSource |
| PUT 校验 providers 约束 | 站点 providers=[] 或删至 0 个 | 400 报错（每站≥1 提供商） |
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
| S11 | 新建站点 | 自动出现同名默认提供商 |
| S12 | 提供商范围设为仅 anthropic | 结果表该提供商列对非 anthropic 模型显示「—」 |
| S13 | 复制提供商改折扣 0.5 | 两列并存且数值不同，互不影响 |
| S14 | 新建第二档案并改配比 | 两档案切换互不干扰；汇总页同时出现两组行 |
| S15 | 汇总页：按换汇排序+筛选厂商 | 排序与过滤正确；范围外/停用节点不出现 |
| S16 | 账户级关分档再开 | 关闭期全按基础档；重开后树内各开关原样恢复 |

## 4. 安全/边界

- 渲染注入：站点名输入 `<img onerror>` → 显示为纯文本（textContent）。
- 超大数值（promptTokens=1e18、价格 1e9）：不崩溃，格式化显示。
- 非法 tiers（末档有限、阈值 0/负）在表单与导入双侧被拒。

## 5. 发布前冒烟（每次交付执行）

`node --check`（全部 js）→ `node --test` → `python -B -m unittest discover -s server -t . -p 'test_*.py'` → Go test/vet/race/tags → S1-S3 → 记录结果于 PR/验收记录。
