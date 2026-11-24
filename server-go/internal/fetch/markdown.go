// markdown 表格/数值解析工具 —— server/fetcher.py 的 Go 移植。
package fetch

import (
	"regexp"
	"strconv"
	"strings"
)

// cells: "| a | b |" → ["a","b"]
func cells(line string) []string {
	s := strings.Trim(strings.TrimSpace(line), "|")
	parts := strings.Split(s, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

var numRe = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)`)

func num(s string) (float64, bool) {
	m := numRe.FindStringSubmatch(strings.ReplaceAll(s, ",", ""))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	return v, err == nil
}

var tokRe = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*([kKmM])`)

// tokens: '500k'/'1M'/'32K' → token 数
func tokens(s string) (int, bool) {
	m := tokRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	mult := 1000.0
	if m[2] == "m" || m[2] == "M" {
		mult = 1_000_000
	}
	return int(v * mult), true
}

// rows: 提取 markdown 表格行（兼容引用块 "> | … |"），跳过 "---" 分隔行
func rows(md string) [][]string {
	var out [][]string
	for _, l := range strings.Split(md, "\n") {
		s := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), ">"))
		if strings.HasPrefix(s, "|") && !strings.Contains(s, "---") {
			out = append(out, cells(s))
		}
	}
	return out
}

// money: '$1.4'/'\$0.26'/'免费'/'不支持' → float64|0|nil
func money(s string) any {
	ts := strings.TrimSpace(s)
	if strings.Contains(s, "免费") || ts == "Free" || ts == "free" {
		return 0.0
	}
	if strings.Contains(s, "不支持") || ts == "-" || ts == "—" {
		return nil
	}
	if v, ok := num(s); ok {
		return v
	}
	return nil
}

var (
	secHeadRe = regexp.MustCompile(`(?m)^#{1,4}\s`)
)

// section: 截取 `### <title>` 到下一个 `###`/`##` 之间的文本
func section(md, title string) string {
	re := regexp.MustCompile(`(?m)^#{2,4}\s+` + regexp.QuoteMeta(title) + `\s*$`)
	loc := re.FindStringIndex(md)
	if loc == nil {
		return ""
	}
	rest := md[loc[1]:]
	end := secHeadRe.FindStringIndex(rest)
	if end == nil {
		return rest
	}
	return rest[:end[0]]
}
