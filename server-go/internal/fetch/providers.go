// 各厂商官方价目 .md 解析器 —— server/fetcher.py 的逐行移植。
package fetch

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Row = 解析出的模型行（对应 Python dict），键序由 marshal.go 规范化。
type Row = map[string]any

// Source 官方价目源定义；Parse 为 nil 表示只探测不解析（人工核对）。
type Source struct {
	URL      string
	Parse    func(string) []Row
	Currency string
}

var Sources = map[string]Source{
	"xai":         {"https://docs.x.ai/developers/pricing.md", ParseXAI, "USD"},
	"deepseek":    {"https://api-docs.deepseek.com/quick_start/pricing.md", ParseDeepSeek, "USD"},
	"zai":         {"https://docs.z.ai/guides/overview/pricing.md", ParseZAI, "USD"},
	"bigmodel-cn": {"https://docs.bigmodel.cn/cn/guide/start/pricing.md", ParseBigmodel, "CNY"},
	// 以下三家页面在部分代理出口可达，达则可自动解析
	"openai":    {"https://developers.openai.com/api/docs/pricing.md", ParseOpenAI, "USD"},
	"anthropic": {"https://platform.claude.com/docs/en/about-claude/pricing.md", ParseAnthropic, "USD"},
	"google":    {"https://aistudio.google.com/docs/pricing.md", ParseGoogle, "USD"},
	// kimi 价格表 JS 渲染，.md 为空壳 → 连通性探测 + 人工核对
	"moonshot": {"https://platform.kimi.com/docs/pricing/chat.md", nil, "CNY"},
}

// SourceOrder 与 Python SOURCES dict 一致的遍历顺序。
var SourceOrder = []string{
	"xai", "deepseek", "zai", "bigmodel-cn", "openai", "anthropic", "google", "moonshot",
}

// ---------------------------------------------------------------- xAI

var xaiNameRe = regexp.MustCompile(`(?i)^(grok-[\w.\-]+)\s*\((<|≥)\s*([\d.]+)\s*k`)

func ParseXAI(md string) []Row {
	models := map[string]Row{}
	var order []string
	for _, c := range rows(md) {
		if len(c) < 5 {
			continue
		}
		m := xaiNameRe.FindStringSubmatch(c[0])
		if m == nil {
			continue
		}
		mid := m[1]
		thr, _ := tokens(m[3] + "k")
		ent, ok := models[mid]
		if !ok {
			ent = Row{"id": mid, "name": mid, "contextWindow": nil, "tiers": []Row{}}
			if cw, ok := tokens(c[1]); ok {
				ent["contextWindow"] = cw
			}
			models[mid] = ent
			order = append(order, mid)
		}
		var up any
		if m[2] == "<" {
			up = thr - 1
		}
		ent["tiers"] = append(ent["tiers"].([]Row), Row{
			"upToTokens": up, "input": money(c[2]),
			"cacheRead": money(c[3]), "output": money(c[4])})
	}
	out := make([]Row, 0, len(order))
	for _, mid := range order {
		ent := models[mid]
		tiers := ent["tiers"].([]Row)
		sort.SliceStable(tiers, func(i, j int) bool {
			return tiers[i]["upToTokens"] != nil && tiers[j]["upToTokens"] == nil
		})
		nt := make([]Row, 0, len(tiers))
		for _, t := range tiers {
			nt = append(nt, Row{"upToTokens": t["upToTokens"], "input": t["input"],
				"output": t["output"], "cacheRead": t["cacheRead"]})
		}
		ent["tiers"] = nt
		out = append(out, ent)
	}
	return out
}

// ---------------------------------------------------------------- Z.AI

func ParseZAI(md string) []Row {
	var out []Row
	for _, c := range rows(md) {
		if len(c) < 5 || !strings.HasPrefix(strings.ToLower(c[0]), "glm-") || strings.Contains(c[0], "Model") {
			continue
		}
		out = append(out, Row{"id": strings.ToLower(c[0]), "name": c[0], "tiers": []Row{{
			"upToTokens": nil, "input": money(c[1]),
			"cacheRead": money(c[2]), "output": money(c[4])}}})
	}
	return out
}

// ---------------------------------------------------------------- DeepSeek

var (
	dsParenRe  = regexp.MustCompile(`\(\d+\)`)
	dsPeakRe   = regexp.MustCompile(`Peak hours are (.+?) UTC`)
	dsWindowRe = regexp.MustCompile(`(\d{2}:\d{2})\s*-\s*(\d{2}:\d{2})`)
)

// ParseDeepSeek 竖表：表头末列为模型名；PRICING 行按 OFF-PEAK/PEAK 给每列模型定价。
func ParseDeepSeek(md string) []Row {
	rs := rows(md)
	var names []string
	start := -1
	for i, c := range rs {
		if len(c) >= 4 && strings.ToUpper(c[0]) == "MODEL" && strings.HasPrefix(strings.ToLower(c[len(c)-1]), "deepseek") {
			for _, x := range c[3:] {
				names = append(names, strings.TrimSpace(dsParenRe.ReplaceAllString(x, "")))
			}
			start = i
			break
		}
	}
	if names == nil {
		return nil
	}
	type key struct{ dim, phase string }
	price := map[string]map[key]any{}
	for _, n := range names {
		price[n] = map[key]any{}
	}
	for _, c := range rs[start:] {
		if len(c) < 3+len(names) || !strings.HasPrefix(c[0], "PRICING") {
			continue
		}
		k := key{strings.ToUpper(c[1]), strings.ToUpper(c[2])}
		for j, n := range names {
			price[n][k] = money(c[3+j])
		}
	}
	peak := func(p map[key]any, k string) any { return p[key{k, "PEAK"}] }
	off := func(p map[key]any, k string) any { return p[key{k, "OFF-PEAK"}] }
	out := make([]Row, 0, len(names))
	for _, n := range names {
		p := price[n]
		ent := Row{"id": n, "name": n, "tiers": []Row{{
			"upToTokens": nil,
			"input":      peak(p, "1M INPUT TOKENS / (CACHE MISS)"),
			"cacheRead":  peak(p, "1M INPUT TOKENS / (CACHE HIT)"),
			"output":     peak(p, "1M OUTPUT TOKENS")}}}
		iOff, iPeak := off(p, "1M INPUT TOKENS / (CACHE MISS)"), peak(p, "1M INPUT TOKENS / (CACHE MISS)")
		if iOff != nil && iPeak != nil {
			if pv, ok := iPeak.(float64); ok && pv != 0 {
				ent["offPeakRatio"] = math.Round(iOff.(float64)/pv*10000) / 10000
				if m := dsPeakRe.FindStringSubmatch(md); m != nil {
					var wins []any
					for _, w := range dsWindowRe.FindAllStringSubmatch(m[1], -1) {
						wins = append(wins, []any{w[1], w[2]})
					}
					if len(wins) > 0 {
						ent["peakWindows"] = wins
						ent["peakNote"] = "UTC 工作日；法定节假日全天闲时（按工作日近似）"
					}
				}
			}
		}
		out = append(out, ent)
	}
	return out
}

// ---------------------------------------------------------------- OpenAI

var (
	oaiNameClean = regexp.MustCompile(`\s*\(<\d+K.*?\)\s*`)
	oaiNameOK    = regexp.MustCompile(`^(gpt-|o\d|chatgpt|codex|computer|gpt-realtime|gpt-audio|gpt-image|whisper|tts|davinci|babbage|text-)`)
)

// ParseOpenAI 仅取 `### Standard pricing data` 节的表（Batch/Flex 等跳过）。
// 11 列 = 短/长上下文档；3 列 = 单档。
func ParseOpenAI(md string) []Row {
	sec := section(md, "Standard pricing data")
	out := map[string]Row{}
	var order []string
	for _, c := range rows(sec) {
		if len(c) < 4 || c[0] == "Model" {
			continue
		}
		name := strings.TrimSpace(oaiNameClean.ReplaceAllString(c[0], ""))
		if !oaiNameOK.MatchString(name) {
			continue
		}
		mid := strings.ToLower(name)
		var tiers []Row
		if len(c) >= 11 {
			tiers = []Row{
				{"upToTokens": 272000, "input": money(c[1]), "output": money(c[2]),
					"cacheRead": money(c[3]),
					"cacheWrites": []Row{
						{"ttl": "30m", "price": money(c[4])},
						{"ttl": "12h", "price": money(c[5])}}},
				{"upToTokens": nil, "input": money(c[6]), "output": money(c[7]),
					"cacheRead": money(c[8]),
					"cacheWrites": []Row{
						{"ttl": "30m", "price": money(c[9])},
						{"ttl": "12h", "price": money(c[10])}}},
			}
			for _, t := range tiers {
				var cw []Row
				for _, w := range t["cacheWrites"].([]Row) {
					if w["price"] != nil {
						cw = append(cw, w)
					}
				}
				if t["cacheRead"] == nil {
					delete(t, "cacheRead")
				}
				if len(cw) > 0 {
					t["cacheWrites"] = cw
				} else {
					delete(t, "cacheWrites")
				}
			}
		} else {
			t := Row{"upToTokens": nil, "input": money(c[1]), "output": money(c[2])}
			if cr := money(c[3]); cr != nil {
				t["cacheRead"] = cr
			}
			tiers = []Row{t}
		}
		if _, ok := out[mid]; !ok {
			order = append(order, mid)
		}
		out[mid] = Row{"id": mid, "name": name, "tiers": tiers}
	}
	res := make([]Row, 0, len(order))
	for _, mid := range order {
		res = append(res, out[mid])
	}
	return res
}

// ---------------------------------------------------------------- Anthropic

var (
	antLinkRe = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	antParRe  = regexp.MustCompile(`\s*\(.*?\)\s*`)
	antUpRe   = regexp.MustCompile(`prompts up to ([\d,]+) tokens`)
)

// ParseAnthropic 主表 6 列：Model | input | 5m write | 1h write | cache hits | output。
func ParseAnthropic(md string) []Row {
	type band struct {
		up any
		c  []string
	}
	type group struct {
		ent   Row
		bands []band
	}
	grouped := map[string]*group{}
	var order []string
	for _, c := range rows(md) {
		if len(c) < 6 || !strings.HasPrefix(c[0], "Claude ") {
			continue
		}
		name := antLinkRe.ReplaceAllString(c[0], "$1")
		status := "active"
		if strings.Contains(name, "retired") {
			status = "retired"
		} else if strings.Contains(name, "limited") {
			status = "limited"
		}
		name = strings.TrimSpace(antParRe.ReplaceAllString(name, " "))
		var up any
		if m := antUpRe.FindStringSubmatch(c[0]); m != nil {
			if v, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", "")); err == nil {
				up = v
			}
		}
		mid := "claude-" + strings.ReplaceAll(strings.ReplaceAll(
			strings.ToLower(strings.TrimPrefix(name, "Claude ")), ".", "-"), " ", "-")
		g, ok := grouped[mid]
		if !ok {
			g = &group{ent: Row{"id": mid, "name": name, "status": status}}
			grouped[mid] = g
			order = append(order, mid)
		}
		g.bands = append(g.bands, band{up, c})
	}
	out := make([]Row, 0, len(order))
	for _, mid := range order {
		g := grouped[mid]
		sort.SliceStable(g.bands, func(i, j int) bool {
			return g.bands[i].up != nil && g.bands[j].up == nil
		})
		multi := len(g.bands) > 1
		tiers := make([]Row, 0, len(g.bands))
		for _, b := range g.bands {
			var up any
			if multi {
				up = b.up
			}
			tiers = append(tiers, Row{
				"upToTokens": up, "input": money(b.c[1]),
				"cacheWrites": []Row{
					{"ttl": "5m", "price": money(b.c[2])},
					{"ttl": "1h", "price": money(b.c[3])}},
				"cacheRead": money(b.c[4]), "output": money(b.c[5])})
		}
		out = append(out, Row{"id": g.ent["id"], "name": g.ent["name"],
			"status": g.ent["status"], "tiers": tiers})
	}
	return out
}

// ---------------------------------------------------------------- Google

var (
	googSecRe   = regexp.MustCompile(`(?m)^##\s+(.+?)\s*\{%\s*id="([\w.\-]+)"\s*%\}`)
	googTabRe   = regexp.MustCompile(`(?s)\{%\s*tab title="Standard"[^%]*%\}(.*?)\{%\s*/tab`)
	googFldRe   = regexp.MustCompile(`^\s*\*\s*(Input price[^\n]*|Output price[^\n]*|Context caching price[^\n]*)\s*$`)
	googPairRe  = regexp.MustCompile(`(?i)\$(\d+(?:\.\d+)?)\s*,?\s*(prompts?\s*(?:<=|<|>|>=)\s*[\d,]+\s*k?)?`)
	googThrRe   = regexp.MustCompile(`([\d,]+)\s*k`)
	googPromoRe = regexp.MustCompile(`through\s+([A-Za-z]+\s+\d+,\s*\d{4})`)
	googWSRe    = regexp.MustCompile(`\s+`)
)

// googFields 三行一组的伪表：`* <字段>` / `* <免费>` / `* <付费>`，付费格可跨行
// （直到下一个 * 或 ---）。对应 Python 里带 lookahead 的正则，改为行扫描。
func googFields(txt string) map[string]string {
	fields := map[string]string{}
	lines := strings.Split(txt, "\n")
	for i := 0; i < len(lines); i++ {
		m := googFldRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		key := "cache"
		if !strings.HasPrefix(m[1], "Context") {
			key = strings.ToLower(strings.SplitN(m[1], " ", 2)[0])
		}
		i++
		if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "*") {
			i++ // 跳过免费档行
		}
		var acc []string
		if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "*") {
			acc = append(acc, strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(lines[i]), "*")))
			i++
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if strings.HasPrefix(t, "*") || strings.HasPrefix(t, "---") {
					break
				}
				acc = append(acc, t)
				i++
			}
		}
		i-- // 外层 for 还会 i++，回退一行避免跳过 ---
		fields[key] = googWSRe.ReplaceAllString(strings.Join(acc, " "), " ")
	}
	return fields
}

// googTiers 解析付费单元格 `$2.00, prompts <= 200k tokens  $4.00, prompts > 200k tokens`
// → (阈值, 短档价, 长档价|nil)；促销只取当前价。
func googTiers(cell string) (any, any, any) {
	var pairs [][2]any
	for _, m := range googPairRe.FindAllStringSubmatch(cell, -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		pairs = append(pairs, [2]any{v, strings.ToLower(m[2])})
	}
	var short, longP any
	var longQ string
	for _, p := range pairs {
		if strings.Contains(p[1].(string), "<") && short == nil {
			short = p[0]
		}
		if strings.Contains(p[1].(string), ">") && longP == nil {
			longP = p[0]
			longQ = p[1].(string)
		}
	}
	var thr any
	if longP != nil {
		if tm := googThrRe.FindStringSubmatch(longQ); tm != nil {
			if t, ok := tokens(tm[0]); ok {
				thr = t
			}
		}
		if short == nil && len(pairs) > 0 {
			short = pairs[0][0]
		}
		return thr, short, longP
	}
	if len(pairs) > 0 {
		short = pairs[0][0]
	}
	return nil, short, nil
}

// ParseGoogle `## <Name> {% id="model-id" %}` 分节 + {% table %} 伪表。
func ParseGoogle(md string) []Row {
	locs := googSecRe.FindAllStringSubmatchIndex(md, -1)
	var out []Row
	for i, loc := range locs {
		disp := strings.TrimSpace(md[loc[2]:loc[3]])
		mid := md[loc[4]:loc[5]]
		end := len(md)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := md[loc[1]:end]
		tab := googTabRe.FindStringSubmatch(body)
		if tab == nil {
			continue
		}
		fields := googFields(tab[1])
		if _, ok := fields["input"]; !ok {
			continue
		}
		if _, ok := fields["output"]; !ok {
			continue
		}
		ti, iv, iv2 := googTiers(fields["input"])
		to, ov, ov2 := googTiers(fields["output"])
		tc, cv, cv2 := googTiers(fields["cache"])
		thr := ti
		if thr == nil {
			thr = to
		}
		if thr == nil {
			thr = tc
		}
		var notes []string
		if promo := googPromoRe.FindStringSubmatch(fields["input"]); promo != nil {
			notes = append(notes, "促销价至 "+promo[1]+"，之后价格见官网")
		}
		t0 := Row{"upToTokens": nil, "input": iv, "output": ov}
		if cv != nil {
			t0["cacheRead"] = cv
		}
		tiers := []Row{t0}
		if thr != nil && iv2 != nil {
			tiers[0]["upToTokens"] = thr
			t1 := Row{"upToTokens": nil, "input": iv2}
			if ov2 != nil {
				t1["output"] = ov2
			} else {
				t1["output"] = ov
			}
			if cv2 != nil {
				t1["cacheRead"] = cv2
			} else if cv != nil {
				t1["cacheRead"] = cv
			}
			tiers = append(tiers, t1)
		}
		if iv == nil || ov == nil {
			continue // 付费列无价的模型不入库
		}
		ent := Row{"id": mid, "name": disp, "tiers": tiers}
		if len(notes) > 0 {
			ent["notes"] = strings.Join(notes, "；")
		}
		if strings.Contains(mid, "preview") {
			ent["status"] = "preview"
		}
		out = append(out, ent)
	}
	return out
}

// ---------------------------------------------------------------- 智谱 bigmodel.cn

var (
	bmNameRe   = regexp.MustCompile(`(?i)^GLM-[\w.\-]+$`)
	bmLoRe     = regexp.MustCompile(`输入(?:长度)?\s*\\?\[\s*(\d+)K?\s*,\s*(\d+)K?\s*\\?\)`)
	bmSingleRe = regexp.MustCompile(`^\s*(\d+)\s*([kKmM])\s*$`)
	bmGeRe     = regexp.MustCompile(`(\d+)K`)
)

// ParseBigmodel 复合分档按 promptTokens 近似：同输入区间内取“输出≥0.2K”行。
func ParseBigmodel(md string) []Row {
	type cand struct {
		ctx                      string
		input, output, cacheRead any
	}
	type group struct {
		ent   Row
		bands map[int][]cand
	}
	grouped := map[string]*group{}
	var order []string
	for _, c := range rows(md) {
		if len(c) < 6 || !bmNameRe.MatchString(c[0]) {
			continue
		}
		name, ctx := c[0], c[1]
		mid := strings.ToLower(name) + "-cn"
		g, ok := grouped[mid]
		if !ok {
			g = &group{ent: Row{"id": mid, "name": name + "（国内）",
				"contextWindow": nil}, bands: map[int][]cand{}}
			grouped[mid] = g
			order = append(order, mid)
		}
		cd := cand{ctx, money(c[2]), money(c[3]), money(c[5])}
		if single := bmSingleRe.FindStringSubmatch(ctx); single != nil {
			if cw, ok := tokens(ctx); ok {
				g.ent["contextWindow"] = cw
			}
			g.bands[0] = append(g.bands[0], cd)
		} else if lo := bmLoRe.FindStringSubmatch(ctx); lo != nil {
			lv, _ := strconv.Atoi(lo[1])
			g.bands[lv*1000] = append(g.bands[lv*1000], cd)
		} else if strings.Contains(ctx, "≥") {
			if m2 := bmGeRe.FindStringSubmatch(ctx); m2 != nil {
				lv, _ := strconv.Atoi(m2[1])
				g.bands[lv*1000] = append(g.bands[lv*1000], cd)
			} else {
				g.bands[0] = append(g.bands[0], cd)
			}
		} else { // '—' 等无区间标记 → 单档
			g.bands[0] = append(g.bands[0], cd)
		}
	}
	out := make([]Row, 0, len(order))
	for _, mid := range order {
		g := grouped[mid]
		los := make([]int, 0, len(g.bands))
		for lo := range g.bands {
			los = append(los, lo)
		}
		sort.Ints(los)
		var tiers []Row
		var notes []string
		for idx, lo := range los {
			cands := g.bands[lo]
			pick := cands[0]
			for _, x := range cands {
				if strings.Contains(x.ctx, "≥0.2K") || strings.Contains(x.ctx, "≥ 0.2K") {
					pick = x
					break
				}
			}
			if len(cands) > 1 {
				var descs []string
				for _, x := range cands {
					descs = append(descs, x.ctx)
				}
				notes = append(notes, "存在按输出长度细分的档位，已取“输出≥0.2K”行："+strings.Join(descs, ";"))
			}
			var up any
			if idx < len(los)-1 {
				up = los[idx+1] - 1
			}
			tiers = append(tiers, Row{"upToTokens": up, "input": pick.input,
				"output": pick.output, "cacheRead": pick.cacheRead})
		}
		row := Row{"id": g.ent["id"], "name": g.ent["name"],
			"contextWindow": g.ent["contextWindow"], "tiers": tiers}
		if len(notes) > 0 {
			row["notes"] = strings.Join(notes, "；")
		}
		out = append(out, row)
	}
	return out
}
