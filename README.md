# ai-price-calculate

AI 中转站价格归一化计算器：把「官方价 × 折扣 × 上下文分档 × token 配比（输入/缓存读/输出）」归一化为可横向比较的每 1M tokens 单价，同时显示不换汇与换汇两个价格。

- 内置 OpenAI / Anthropic / Gemini / xAI / DeepSeek / 智谱(Z.AI+bigmodel.cn) / Kimi 官方价目（`data/models.json`，含上下文分档与缓存价）
- 自定义中转站：站级默认折扣、模型级折扣、模型级自定义基础价（含自定义分档）
- 汇率：手动设置或联网自动获取（USD↔CNY）
- 技术：纯 HTML+JS 前端 + Node 零依赖后端，无构建、无第三方包

```bash
npm start        # node server/index.js → http://127.0.0.1:8787
npm test         # node --test tests/
```

文档入口：`AGENTS.md` → `docs/PRD.md`、`docs/ARCHITECTURE.md`（当前处发现阶段，实现待批准）。
