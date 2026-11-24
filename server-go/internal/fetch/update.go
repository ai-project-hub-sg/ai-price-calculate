// 官方价目抓取与合并编排 —— server/fetcher.py update() 的移植。
// 直连 → 代理订阅节点池（可选）→ 解析 → 合并写回 models.json。
// 合并只更新厂商价目（tiers/fetchedAt/checkedAt），从不写 config.json。
package fetch

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/proxypool"
)

type Options struct {
	Providers  []string // 空 = 全部源
	MaxNodes   int      // 探测节点上限，默认 25
	SubURL     string   // 代理订阅地址（空 = 仅直连）
	ModelsPath string
	DryRun     bool
	Log        func(string)
}

// minPricingBytes 判定"页面真实抓到"的最小字节数——Kimi 等 JS 渲染页
// 返回的空壳 .md 只有几百 B，低于此阈值视为未抓到（显式阈值，勿静默改）。
const minPricingBytes = 500

func Today() string { return time.Now().Format("2006-01-02") }

func Update(o Options) (map[string]any, error) {
	log := o.Log
	if log == nil {
		log = func(string) {}
	}
	targets := o.Providers
	if len(targets) == 0 {
		targets = SourceOrder
	}
	maxNodes := o.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 25
	}
	subDesc := "未配置代理订阅 → 仅直连"
	if o.SubURL != "" {
		subDesc = "直连失败 → 代理订阅节点池重试"
	}
	log(fmt.Sprintf("更新 %d 个源：%s", len(targets), subDesc))
	dataset, err := LoadDataset(o.ModelsPath)
	if err != nil {
		return nil, err
	}
	today := Today()
	report := map[string]any{"fetchedAt": today, "providers": map[string]any{}}
	provs := report["providers"].(map[string]any)

	fetched := map[string]string{}
	needProxy := map[string]string{}

	for _, pid := range targets {
		src, ok := Sources[pid]
		if !ok { // 显式报告未知源，不静默跳过
			provs[pid] = map[string]any{"via": "none", "status": 0,
				"error": "未知 provider id（不在 SOURCES）"}
			log(fmt.Sprintf("[%s] 未知 provider id，跳过", pid))
			continue
		}
		r := proxypool.FetchURL(src.URL, 0, 12_000)
		pr := map[string]any{"via": "direct", "status": r.Status}
		if r.Status == 200 && r.Bytes > minPricingBytes {
			fetched[pid] = string(r.Body)
			log(fmt.Sprintf("[%s] 直连成功 %dB", pid, r.Bytes))
		} else {
			needProxy[pid] = src.URL
			if r.Error != "" {
				pr["error"] = r.Error
			}
			errDesc := r.Error
			if errDesc == "" {
				errDesc = fmt.Sprintf("HTTP %d", r.Status)
			}
			log(fmt.Sprintf("[%s] 直连失败 %s，转节点池", pid, errDesc))
		}
		provs[pid] = pr
	}

	if len(needProxy) > 0 {
		if o.SubURL == "" {
			log(fmt.Sprintf("%d 个源直连失败且未配置代理订阅地址，标记不可达", len(needProxy)))
			report["unreachable"] = sortedMapKeys(needProxy)
		} else {
			nodes, err := proxypool.LoadSubscription(o.SubURL, log)
			if err != nil {
				log("代理订阅拉取失败: " + err.Error())
				report["unreachable"] = sortedMapKeys(needProxy)
				for pid := range needProxy {
					provs[pid].(map[string]any)["error"] = "直连失败；订阅拉取失败: " + err.Error()
				}
			} else {
				log(fmt.Sprintf("节点池探测 %d 个目标（订阅节点 %d 个，上限 %d）…",
					len(needProxy), len(nodes), maxNodes))
				probe := proxypool.FindWorking(nodes, needProxy, maxNodes, 4, 8_000,
					func(tested int, pending []string) {
						log(fmt.Sprintf("  已测 %d 节点，待解 %d", tested, len(pending)))
					})
				for pid, win := range probe.Winners {
					r := proxypool.FetchThrough(win.Node, Sources[pid].URL, 15_000)
					pr := provs[pid].(map[string]any)
					pr["via"] = "node:" + win.Label
					pr["status"] = r.Status
					if r.Status == 200 && r.Bytes > minPricingBytes {
						fetched[pid] = string(r.Body)
						delete(pr, "error")
						log(fmt.Sprintf("[%s] 经节点 %s 成功 %dB", pid, win.Label, r.Bytes))
					} else if r.Error != "" {
						pr["error"] = r.Error
					}
				}
				report["unreachable"] = probe.Pending
			}
		}
	}

	changed := map[string]any{}
	for _, pid := range targets {
		mdText, ok := fetched[pid]
		if !ok {
			continue
		}
		src := Sources[pid]
		pr := provs[pid].(map[string]any)
		if src.Parse == nil {
			pr["action"] = "manual-check"
			log(fmt.Sprintf("[%s] 页面可达但无自动解析器 → 人工核对", pid))
			continue
		}
		parsed, perr := safeParse(src.Parse, mdText)
		if perr != nil {
			pr["action"] = "parse-error"
			pr["error"] = perr.Error()
			continue
		}
		if len(parsed) == 0 {
			pr["action"] = "parse-empty"
			continue
		}
		res := dataset.MergeProvider(pid, parsed, today)
		changed[pid] = res
		pr["action"] = "merged"
		pr["updated"] = len(strList(res["updated"]))
		pr["added"] = len(strList(res["added"]))
		pr["skippedRetired"] = len(strList(res["skippedRetired"]))
		pr["missingInSource"] = len(strList(res["missingInSource"]))
		log(fmt.Sprintf("[%s] 合并：更新 %d，新增 %d，源中缺失 %d",
			pid, pr["updated"], pr["added"], pr["missingInSource"]))
	}

	if len(changed) > 0 && !o.DryRun {
		// 对齐 Python：备份失败则中止，禁止无备份覆盖
		if bak, err := os.ReadFile(o.ModelsPath); err != nil {
			return nil, fmt.Errorf("读 models.json 备份源失败: %w", err)
		} else if err := os.WriteFile(o.ModelsPath+".bak", bak, 0o644); err != nil {
			return nil, fmt.Errorf("写 models.json.bak 失败: %w", err)
		}
		out, err := dataset.Marshal()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(o.ModelsPath, out, 0o644); err != nil {
			return nil, err
		}
		log("已写回 models.json（备份 models.json.bak）")
	} else if o.DryRun {
		log("dry-run：不写回")
	}
	report["changed"] = changed
	return report, nil
}

func safeParse(fn func(string) []Row, md string) (rows []Row, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn(md), nil
}

func strList(v any) []string {
	if l, ok := v.([]string); ok {
		return l
	}
	return nil
}

func sortedMapKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// MergeProvider 合并解析行到 dataset.providers[pid] —— server/fetcher.py merge_provider 移植。
func (d *Dataset) MergeProvider(providerID string, rows []Row, today string) map[string]any {
	prov := d.Provider(providerID)
	if prov == nil {
		return map[string]any{"error": "provider " + providerID + " 不在数据集中"}
	}
	existing := map[string]map[string]any{}
	models, _ := prov["models"].([]any)
	for _, m := range models {
		if mm, ok := m.(map[string]any); ok {
			if id, ok := mm["id"].(string); ok {
				existing[id] = mm
			}
		}
	}
	var updated, added, skippedRetired []string
	rowIDs := map[string]bool{}
	for _, row := range rows {
		mid, _ := row["id"].(string)
		rowIDs[mid] = true
		if m, ok := existing[mid]; ok {
			m["tiers"] = row["tiers"]
			if cw, ok2 := row["contextWindow"]; ok2 && cw != nil {
				m["contextWindow"] = cw
			}
			if s, ok2 := row["status"].(string); ok2 && s != "" {
				m["status"] = s
			}
			if n, ok2 := row["notes"].(string); ok2 && n != "" {
				m["notes"] = n
			}
			m["fetchedAt"] = today
			updated = append(updated, mid)
		} else if row["status"] == "retired" {
			skippedRetired = append(skippedRetired, mid)
		} else {
			if _, ok2 := row["status"]; !ok2 {
				row["status"] = "active"
			}
			row["fetchedAt"] = today
			if _, ok2 := row["officialUpdatedAt"]; !ok2 {
				row["officialUpdatedAt"] = nil
			}
			if _, ok2 := row["notes"]; !ok2 {
				row["notes"] = "自动更新新增，请人工核对"
			}
			prov["models"] = append(models, row)
			models = prov["models"].([]any)
			added = append(added, mid)
		}
	}
	var missing []string
	for mid := range existing {
		if !rowIDs[mid] {
			missing = append(missing, mid)
		}
	}
	prov["checkedAt"] = today
	return map[string]any{"updated": updated, "added": added,
		"skippedRetired": skippedRetired, "missingInSource": missing}
}
