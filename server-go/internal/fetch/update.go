// 官方价目抓取与合并编排 —— server/fetcher.py update() 的移植。
// 直连 → 代理订阅节点池（可选）→ 解析 → 合并写回 models.json。
// 合并只更新厂商价目（tiers/fetchedAt/checkedAt），从不写 config.json。
package fetch

import (
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/ai-project-hub-sg/ai-price-calculate/server-go/internal/proxypool"
)

// Progress 结构化进度（job.progress 下发前端）：阶段 + 全部厂家状态列表 +
// 当前组合（节点→厂家）+ 当前项开始时间——前端据此渲染厂家徽章行与组合耗时，
// "谁在抓/谁卡住/还剩谁"一目了然。
type Progress struct {
	Phase     string   `json:"phase"`              // direct|sub|warm|probe|merge
	Item      string   `json:"item,omitempty"`     // 当前活动：direct=厂家id，节点阶段="节点 → 厂家"
	Current   []string `json:"current"`            // 正在抓取的厂家 id 集（并发节点各抓一个，全亮）
	ItemIndex int      `json:"itemIndex,omitempty"`
	ItemTotal int      `json:"itemTotal,omitempty"`
	Targets   []string `json:"targets"`            // 全部价目源（有序）
	Done      []string `json:"done"`               // 已解决厂家 id
	StartedAt int64    `json:"startedAt"`          // 当前项开始 epoch ms（前端算本项耗时）
}

type Options struct {
	Providers  []string // 空 = 全部源
	MaxNodes   int      // 探测节点上限，默认 25
	SubURL     string   // 代理订阅地址（空 = 仅直连）
	ModelsPath string
	DryRun     bool
	Log        func(string)
	NodeCache  *proxypool.Cache // 非nil启用节点可用性缓存（温路径复用）
	Progress   func(Progress)   // 非nil接收结构化进度事件
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
	fetched := map[string]string{}
	needProxy := map[string]string{}
	doneSet := map[string]bool{} // 已解决：直连抓到（fetched）或未知源跳过
	inflight := map[string]int{} // 正在抓取的厂家（计数：并发节点可同抓一个）
	var pMu sync.Mutex           // inflight 从探测 goroutine 并发改——必须加锁
	bump := func(k string, d int) {
		pMu.Lock()
		inflight[k] += d
		if inflight[k] <= 0 {
			delete(inflight, k)
		}
		pMu.Unlock()
	}

	emit := func(p Progress) {
		// 统一填充：Targets=全部源；Done=已解决厂家 id；Current=在飞厂家集。
		p.Targets = targets
		p.Done = []string{} // 对齐规则：列表不输出 null
		for _, t := range targets {
			if doneSet[t] {
				p.Done = append(p.Done, t)
			}
		}
		p.Current = []string{}
		pMu.Lock()
		for k := range inflight {
			p.Current = append(p.Current, k)
		}
		pMu.Unlock()
		sort.Strings(p.Current)
		p.StartedAt = time.Now().UnixMilli()
		if o.Progress != nil {
			o.Progress(p)
		}
	}
	log(fmt.Sprintf("更新 %d 个源：%s", len(targets), subDesc))
	dataset, err := LoadDataset(o.ModelsPath)
	if err != nil {
		return nil, err
	}
	today := Today()
	report := map[string]any{"fetchedAt": today, "providers": map[string]any{}}
	provs := report["providers"].(map[string]any)

	for i, pid := range targets {
		src, ok := Sources[pid]
		if !ok { // 显式报告未知源，不静默跳过
			provs[pid] = map[string]any{"via": "none", "status": 0,
				"error": "未知 provider id（不在 SOURCES）"}
			doneSet[pid] = true
			log(fmt.Sprintf("[%s] 未知 provider id，跳过", pid))
			continue
		}
		bump(pid, +1)
		emit(Progress{Phase: "direct", Item: pid,
			ItemIndex: i + 1, ItemTotal: len(targets)})
		r := proxypool.FetchURL(src.URL, 0, 12_000)
		bump(pid, -1)
		pr := map[string]any{"via": "direct", "status": r.Status}
		if r.Status == 200 && r.Bytes > minPricingBytes {
			body := string(r.Body)
			if src.Parse != nil && parsesEmpty(src.Parse, body) {
				// 200 但解析为空 = 地区跳转/反爬墙页——立即判未抓到转节点池，
				// 与传输层失败同级；解析 panic 不算空（merge 阶段报 parse-error）。
				needProxy[pid] = src.URL
				pr["error"] = "直连返回内容解析为空（疑似地区限制/反爬页）"
				log(fmt.Sprintf("[%s] 直连成功 %dB 但解析为空，转节点池", pid, r.Bytes))
			} else {
				fetched[pid] = body
				doneSet[pid] = true
				log(fmt.Sprintf("[%s] 直连成功 %dB", pid, r.Bytes))
			}
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
			// 节点列表也进缓存：订阅端点本身会超时/抖动，1h 内不必次次重拉；
			// 拉取失败时回退到过期副本兜底，不至于整轮更新报废。
			var nodes []proxypool.Node
			if o.NodeCache != nil {
				nodes = o.NodeCache.SubNodes(o.SubURL)
				if len(nodes) > 0 {
					log(fmt.Sprintf("订阅节点列表缓存命中：%d 节点", len(nodes)))
				}
			}
			if len(nodes) == 0 {
				emit(Progress{Phase: "sub"})
				loaded, lerr := proxypool.LoadSubscription(o.SubURL, log)
				if lerr == nil {
					nodes = loaded
					if o.NodeCache != nil {
						o.NodeCache.MarkSub(o.SubURL, nodes)
					}
				} else if o.NodeCache != nil {
					if stale := o.NodeCache.StaleSubNodes(o.SubURL); len(stale) > 0 {
						log(fmt.Sprintf("订阅拉取失败（%s），回退缓存节点列表 %d 个（可能过期）",
							lerr.Error(), len(stale)))
						nodes = stale
					}
				}
				if len(nodes) == 0 {
					log("代理订阅拉取失败: " + lerr.Error())
					report["unreachable"] = sortedMapKeys(needProxy)
					for pid := range needProxy {
						provs[pid].(map[string]any)["error"] = "直连失败；订阅拉取失败: " + lerr.Error()
					}
				}
			}
			if len(nodes) > 0 {
				// 胜出校验：200 之外还要能解析出价目行——地区限制/反爬页也返回 200。
				validate := func(pid string, body []byte) bool {
					src := Sources[pid]
					if src.Parse == nil {
						return true
					}
					rows, err := safeParse(src.Parse, string(body))
					return err == nil && len(rows) > 0
				}
				winners := map[string]proxypool.Winner{}
				probed := map[string]bool{} // 本 run 已试节点（Key），冷路径不重复
				// 温路径：1h 内测过的可用节点直接复用，逐目标「抓+校验」，
				// 不重跑全池探测（点过「测试节点」再更新就靠这层省）。
				if o.NodeCache != nil {
					if usable := o.NodeCache.Usable(o.SubURL, nodes); len(usable) > 0 {
						log(fmt.Sprintf("节点缓存命中：%d 个近期可用节点直接复用", len(usable)))
						for i, n := range usable {
							if len(needProxy) == 0 {
								break
							}
							probed[proxypool.Key(n)] = true
							lb, lerr := proxypool.StartNode(n)
							if lerr != nil {
								o.NodeCache.Mark(o.SubURL, n, false, 0, lerr.Error())
								log(fmt.Sprintf("  缓存节点 %s 已失效，顺延下一节点", n.Label()))
								continue
							}
							won := false
							for pid, u := range needProxy {
								bump(pid, +1)
								emit(Progress{Phase: "warm",
									Item: n.Label() + " → " + pid,
									ItemIndex: i + 1, ItemTotal: len(usable)})
								r := proxypool.FetchURL(u, lb.Port, 15_000)
								bump(pid, -1)
								if r.Status == 200 && r.Error == "" && validate(pid, r.Body) {
									winners[pid] = proxypool.Winner{Node: n, Label: n.Label(), Body: r.Body}
									delete(needProxy, pid)
									won = true
								}
							}
							lb.Close()
							if won {
								o.NodeCache.Mark(o.SubURL, n, true, 0, "")
							}
						}
					}
				}
				if len(needProxy) > 0 {
					rest := []proxypool.Node{}
					for _, n := range nodes {
						if !probed[proxypool.Key(n)] {
							rest = append(rest, n)
						}
					}
					log(fmt.Sprintf("节点池探测 %d 个目标（候选节点 %d 个，上限 %d）…",
						len(needProxy), len(rest), maxNodes))
					probe := proxypool.FindWorking(rest, needProxy, maxNodes, 4, 8_000, validate,
						proxypool.ProbeHooks{
							OnNodeStart: func(label string, idx, total int) {
								emit(Progress{Phase: "probe", Item: label,
									ItemIndex: idx, ItemTotal: total})
							},
							OnTarget: func(nodeLabel, key string) {
								bump(key, +1)
								emit(Progress{Phase: "probe",
									Item: nodeLabel + " → " + key})
							},
							OnTargetDone: func(nodeLabel, key string) {
								bump(key, -1)
							},
							OnProgress: func(tested int, pending []string) {
								log(fmt.Sprintf("  已测 %d 节点，待解 %d", tested, len(pending)))
							},
						})
					if o.NodeCache != nil {
						o.NodeCache.Absorb(o.SubURL, probe.Reports)
					}
					for pid, win := range probe.Winners {
						winners[pid] = win
					}
				}
				for pid, win := range winners {
					delete(needProxy, pid) // 胜出即不再算不可达（冷路径删的是 FindWorking 内部副本）
					pr := provs[pid].(map[string]any)
					pr["via"] = "node:" + win.Label
					body := win.Body // 探测时已校验非空解析，直接复用
					if len(body) == 0 {
						r := proxypool.FetchThrough(win.Node, Sources[pid].URL, 15_000)
						body = r.Body
					}
					if len(body) > minPricingBytes {
						fetched[pid] = string(body)
						doneSet[pid] = true
						delete(pr, "error")
						log(fmt.Sprintf("[%s] 经节点 %s 成功 %dB", pid, win.Label, len(body)))
					}
				}
				report["unreachable"] = sortedMapKeys(needProxy)
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
		emit(Progress{Phase: "merge"})
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

// parsesEmpty 判"页面虽返回 200/>500B 但解析不出任何价目行"——地区限制、
// 反爬跳转页的典型特征。解析异常（perr!=nil）返回 false，由 merge 阶段报 parse-error。
func parsesEmpty(fn func(string) []Row, body string) bool {
	rows, err := safeParse(fn, body)
	return err == nil && len(rows) == 0
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
	updated, added, skippedRetired := []string{}, []string{}, []string{}
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
	missing := []string{}
	for mid := range existing {
		if !rowIDs[mid] {
			missing = append(missing, mid)
		}
	}
	prov["checkedAt"] = today
	return map[string]any{"updated": updated, "added": added,
		"skippedRetired": skippedRetired, "missingInSource": missing}
}
