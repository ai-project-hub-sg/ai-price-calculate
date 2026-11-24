/* 演示数据：仅当无用户配置（localStorage 与后端均为空）时载入。
 * 纯 JSON 字面量，file:// 与后端模式都可用。 */
"use strict";
window.DEMO_CONFIG = {
  version: 2,
  activeProfileId: "pf-demo-main",
  profiles: [
    {
      id: "pf-demo-main", name: "主力测算", notes: "演示档案：日常对话场景",
      exchangeRate: { mode: "auto", manualValue: null, auto: { value: null, source: null, fetchedAt: null } },
      scenario: {
        promptTokens: 128000,
        mix: { input: 0.45, cacheRead: 0.3, cacheWrite: 0.05, output: 0.2 },
        offPeak: false,
        presets: [
          { id: "pp-1", name: "对话均衡", promptTokens: 128000, mix: { input: 0.45, cacheRead: 0.3, cacheWrite: 0.05, output: 0.2 } },
          { id: "pp-2", name: "高缓存读", promptTokens: 128000, mix: { input: 0.25, cacheRead: 0.6, cacheWrite: 0.05, output: 0.1 } }
        ],
        activePreset: null
      },
      tieredPricing: { enabled: true },
      stations: [
        {
          id: "st-a", name: "示例站A", url: "https://api.station-a.example",
          enabled: true, defaultDiscount: 0.8, notes: "综合站",
          tieredPricing: { enabled: true },
          providers: [
            {
              id: "pr-a1", name: "示例站A", isDefault: true, enabled: true,
              defaultDiscount: null, tieredPricing: { enabled: true },
              scope: { vendorIds: [], series: [], modelIds: [] },
              vendorSwitches: [], modelOverrides: []
            },
            {
              id: "pr-a2", name: "批发上游", isDefault: false, enabled: true,
              defaultDiscount: 0.7, tieredPricing: { enabled: true },
              scope: { vendorIds: ["anthropic", "openai"], series: [], modelIds: [] },
              vendorSwitches: [
                { vendorId: "anthropic", tieredPricing: { enabled: false } }
              ],
              modelOverrides: [
                { id: "ov-1", modelId: "claude-sonnet-5-5", discount: 0.65, tieredPricing: { enabled: true } },
                {
                  id: "ov-2", modelId: "gpt-6.2-astra", discount: null, tieredPricing: { enabled: true },
                  basePrice: { tiers: [
                    { upToTokens: 272000, input: 7.2, output: 36, cacheRead: 0.36 },
                    { upToTokens: null, input: 14, output: 55, cacheRead: 0.7 }
                  ] }
                }
              ]
            },
            {
              id: "pr-a3", name: "GLM专线", isDefault: false, enabled: true,
              defaultDiscount: 0.75, tieredPricing: { enabled: true },
              mix: { input: 0.25, cacheRead: 0.6, cacheWrite: 0.05, output: 0.1 },
              scope: { vendorIds: ["zai", "bigmodel-cn"], series: [], modelIds: [] },
              vendorSwitches: [], modelOverrides: []
            }
          ]
        },
        {
          id: "st-b", name: "便宜站B", url: "", enabled: true, defaultDiscount: 0.7, notes: "",
          tieredPricing: { enabled: true },
          providers: [
            {
              id: "pr-b1", name: "便宜站B", isDefault: true, enabled: true,
              defaultDiscount: null, tieredPricing: { enabled: true },
              scope: { vendorIds: [], series: [], modelIds: [] },
              vendorSwitches: [],
              modelOverrides: [
                { id: "ov-3", modelId: "kimi-k3", discount: 0.8, tieredPricing: { enabled: true },
                  mix: { input: 0.6, cacheRead: 0.1, cacheWrite: 0.1, output: 0.2 } },
                { id: "ov-4", modelId: "deepseek-v4-pro", discount: 0.6, tieredPricing: { enabled: true } }
              ]
            }
          ]
        },
        {
          id: "st-off", name: "停用站示例", url: "", enabled: false, defaultDiscount: 0.5, notes: "停用后不产行",
          tieredPricing: { enabled: true },
          providers: [
            { id: "pr-x1", name: "停用站示例", isDefault: true, enabled: true, defaultDiscount: null,
              tieredPricing: { enabled: true }, scope: { vendorIds: [], series: [], modelIds: [] },
              vendorSwitches: [], modelOverrides: [] }
          ]
        }
      ],
      modelOverrides: [
        { id: "gov-1", modelId: "gemini-3.8-flash", discount: null,
          tieredPricing: { enabled: true },
          basePrice: { tiers: [{ upToTokens: null, input: 0.7, output: 3.5, cacheRead: 0.07 }] } }
      ],
      display: { precision: 4, targetCurrency: "CNY" }
    },
    {
      id: "pf-demo-cheap", name: "低价对比", notes: "演示档案：长上下文+非高峰",
      exchangeRate: { mode: "auto", manualValue: null, auto: { value: null, source: null, fetchedAt: null } },
      scenario: {
        promptTokens: 300000,
        mix: { input: 0.85, cacheRead: 0.05, cacheWrite: 0.05, output: 0.05 },
        offPeak: true,
        presets: [
          { id: "pp-3", name: "长文写作", promptTokens: 300000, mix: { input: 0.85, cacheRead: 0.05, cacheWrite: 0.05, output: 0.05 } }
        ],
        activePreset: null
      },
      tieredPricing: { enabled: true },
      stations: [
        {
          id: "st-c", name: "聚合站C", url: "", enabled: true, defaultDiscount: 0.6, notes: "",
          tieredPricing: { enabled: true },
          providers: [
            {
              id: "pr-c1", name: "聚合站C", isDefault: true, enabled: true,
              defaultDiscount: null, tieredPricing: { enabled: true },
              scope: { vendorIds: ["openai", "google", "xai"], series: [], modelIds: [] },
              vendorSwitches: [], modelOverrides: []
            }
          ]
        },
        {
          id: "st-d", name: "国内专线", url: "", enabled: true, defaultDiscount: 0.85, notes: "人民币渠道",
          tieredPricing: { enabled: true },
          providers: [
            {
              id: "pr-d1", name: "国内专线", isDefault: true, enabled: true,
              defaultDiscount: null, tieredPricing: { enabled: true },
              scope: { vendorIds: ["moonshot", "bigmodel-cn"], series: [], modelIds: [] },
              vendorSwitches: [],
              modelOverrides: [
                { id: "ov-5", modelId: "kimi-k3", discount: 0.75, tieredPricing: { enabled: true } }
              ]
            },
            {
              id: "pr-d2", name: "智谱渠道", isDefault: false, enabled: true,
              defaultDiscount: 0.8, tieredPricing: { enabled: true },
              scope: { vendorIds: ["bigmodel-cn"], series: ["glm-4.x"], modelIds: [] },
              vendorSwitches: [], modelOverrides: []
            }
          ]
        }
      ],
      modelOverrides: [],
      display: { precision: 4, targetCurrency: "CNY" }
    },
    {
      id: "pf-demo-orig", name: "官方原价对照", notes: "所有折扣=1，行价应≈官方列",
      exchangeRate: { mode: "auto", manualValue: null, auto: { value: null, source: null, fetchedAt: null } },
      scenario: {
        promptTokens: 128000,
        mix: { input: 0.45, cacheRead: 0.3, cacheWrite: 0.05, output: 0.2 },
        offPeak: false, presets: [], activePreset: null
      },
      tieredPricing: { enabled: true },
      stations: [
        {
          id: "st-e", name: "原价站", url: "", enabled: true, defaultDiscount: 1, notes: "",
          tieredPricing: { enabled: true },
          providers: [
            { id: "pr-e1", name: "原价站", isDefault: true, enabled: true, defaultDiscount: null,
              tieredPricing: { enabled: true },
              scope: { vendorIds: [], series: [], modelIds: [] },
              vendorSwitches: [], modelOverrides: [] }
          ]
        }
      ],
      modelOverrides: [],
      display: { precision: 4, targetCurrency: "CNY" }
    }
  ]
};
