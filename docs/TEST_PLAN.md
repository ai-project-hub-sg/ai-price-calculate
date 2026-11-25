# TEST_PLAN — 测试策略与用例

范围：单用户本地 Web 工具；当前已有管理员登录/Bearer 鉴权、受限 API 与后台任务。性能测试从简（数据量 <100 模型 × <20 站，纯前端计算非瓶颈）。重点是**计算正确性、配置健壮性与凭证保护**。

## 1. 前端自动测试 `tests/validation.test.js`（node:test，无 DOM）

| 用例 | 期望 |
|---|---|
| 空代理 URL | 允许并规范化为空串 |
| http/https URL | trim 后返回规范化地址 |
| URL 长度 | 按 UTF-8 字节计，上限 4096；多字节 Unicode 有回归用例 |
| 非法 URL | 非字符串、缺协议、空白、非法百分号编码均拒绝 |

计算核心用例（档位、配比、折扣链、换汇、汇总等）仍需补自动化测试；原计划用例继续作为人工验收依据。

## 2. 后端自动测试

- Python：`python -B -m unittest discover -s server -t . -p "test_*.py"`。覆盖登录/退出、限流、Windows 凭据读取 mock、配置原子写、URL 校验、job/停机、汇率刷新与缓存校验、价目合并/备份失败/并发串行化。
- Go：`cd server-go && go test ./...`，协议路径另跑 `go test -tags "with_utls with_quic" ./...`。覆盖 HTTP/鉴权、job 快照与 panic、FX 缓存、解析器 Python 对拍、merge/写回、订阅 Base64/URI 字段与凭证脱敏、Transport 清理、节点探测 panic 恢复。
- `npm test` 组合运行 Node 与 Python；Go 测试不在 npm 脚本中，须单独执行。

## 3. API 验收要点（当前由 Python/Go 离线集成测试覆盖）

| 用例 | 期望 |
|---|---|
| GET /api/config（无文件） | 返回默认配置结构 |
| PUT /api/config 合法 | 200 且文件原子落盘；GET 回读一致 |
| PUT 非法 JSON/URL/空对象 | 400，原文件不被破坏 |
| GET /api/models | 返回 providers 数组；文件损坏返回 500 |
| GET /api/fx | 有效缓存命中；`refresh=1` 强制上游；无效汇率返回 502 且不缓存 |
| GET /api/nodes | 返回候选节点清单且无任何凭据字段 |
| POST /api/pricing/update、/api/nodes/test | 非法 providers/maxNodes 返回 400 且不创建 job；合法请求返回 jobId |
| GET /api/jobs/<id> | 返回 status/report/log/progress/startedAt 快照 |
| 鉴权与静态服务 | 除登录/OPTIONS/静态资源外 API 要求 Bearer；路径穿越被拒 |

## 4. 手工冒烟 / E2E 清单（Demo 与验收门执行并记录）

| # | 步骤 | 期望 |
|---|---|---|
| S0 | 使用环境变量/凭据管理器配置管理员后启动，错误密码→正确密码→退出 | 错误密码 401、连续失败触发 429；正确登录进入页面；退出后 API 返回 401 |
| S1 | `npm start` 打开首页 | 场景栏+空态引导可见，无控制台报错 |
| S2 | 新增示例站（0.8 折） | 结果表出现该站列，数值=官方×0.8 |
| S3 | 站内覆盖模型折扣 0.5 | 该模型行单独变价，其余不变 |
| S4 | 站内自定义基础价含分档 | 改 promptTokens 跨档时该格按自定义档跳变 |
| S5 | 配比 70/20/10 → 70/30/0 | 缓存读相关项下降、排序变化正确 |
| S6 | 汇率手动 7.0 → 自动获取 → 手动刷新 | 显示来源/时间；手动刷新确实重新请求上游；关网刷新→stale 提示不白屏 |
| S7 | 刷新页面 | 配置全保留（后端模式） |
| S7b | 让 PUT /api/config 返回 400/500 后修改配置 | 页面显示「配置保存失败」，不得静默显示成功 |
| S8 | 杀后端重开页面 | 降级横幅 + localStorage 配置仍在 |
| S9 | 导出→清配置→导入 | 完整还原；手改 JSON 破坏字段→逐条报错 |
| S10 | file:// 双击 index.html | 可算价（自动汇率不可用→手动），降级正常 |
| S11 | 新建站点 | 自动出现同名默认提供商 |
| S12 | 提供商范围设为仅 anthropic | 结果表该提供商列对非 anthropic 模型显示「—」 |
| S13 | 复制提供商改折扣 0.5 | 两列并存且数值不同，互不影响 |
| S14 | 新建第二档案并改配比 | 两档案切换互不干扰；汇总页同时出现两组行 |
| S15 | 汇总页：按换汇排序+筛选厂商 | 排序与过滤正确；范围外/停用节点不出现 |
| S16 | 账户级关分档再开 | 关闭期全按基础档；重开后树内各开关原样恢复 |

## 5. 安全/边界

- 渲染注入：站点名输入 `<img onerror>` → 显示为纯文本（textContent）。
- 超大数值（promptTokens=1e18、价格 1e9）：不崩溃，格式化显示。
- 非法 tiers（末档有限、阈值 0/负）在表单与导入双侧被拒。

## 6. 发布前冒烟（每次交付执行）

`node --check`（全部 js）→ `npm test` → `cd server-go && go test ./...` → tagged/race/vet → Linux/Windows tagged 构建与 embed 一致性 → S0–S3、S6–S8 → 记录结果于 PR/验收记录。自动测试不替代登录后真实 UI、Windows 凭据管理器、Linux VPS 与真实订阅验收。
