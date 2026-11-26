"""重新生成 server-go 测试基准（testdata/*.expected.json、merge_*）。

用法（仓库根目录）：
    python server-go/tools/gen_fixtures.py

基准全部由 Python 参考实现产出，Go 测试做 JSON 归一化后深度比对：
  - *.expected.json      各厂商 .md fixture 经 Python 解析器的输出行
  - merge_input.json     从真实 data/models.json 抽取的切片（meta + deepseek 裁剪 + xai 原样）
  - merge_expected.json  merge_input 经 Python merge_provider 后的完整数据集 + 合并报告
  - merge_expected_file.json  Python 对 merge_input 执行 merge 后写回磁盘的
                         原始字节——Go 写回做逐字节对拍（数字格式/键序/缩进的
                         严格守门，语义比较看不出的 64000 vs 64000.0 就靠它）
TODAY 固定为 2026-11-24 保证确定性。
所有文件以 LF 写出（testdata/.gitattributes 固定 eol=lf）——字节对拍
不得因 Windows/Linux 检出换行差异而平台相关。
"""
import copy
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))

from server import fetcher  # noqa: E402

fetcher.TODAY = "2026-11-24"  # 固定日期 → 确定性基准

TD = ROOT / "server-go" / "internal" / "fetch" / "testdata"
FIXTURES = {  # fixture 名 → Python 解析器
    "xai": fetcher.parse_xai,
    "ds": fetcher.parse_deepseek,
    "zai": fetcher.parse_zai,
    "bm": fetcher.parse_bigmodel,
    "openai": fetcher.parse_openai,
    "anthropic": fetcher.parse_anthropic,
    "google": fetcher.parse_google,
}


def dump(obj, path: Path):
    # newline="\n" 固定 LF：Windows 上 write_text 默认把 \n 翻成 CRLF，
    # 会导致字节对拍平台相关（testdata/.gitattributes 同步固定 eol=lf）
    path.write_text(json.dumps(obj, ensure_ascii=False, indent=2),
                    encoding="utf-8", newline="\n")
    print("生成", path.relative_to(ROOT))


def main():
    for name, parse in FIXTURES.items():
        md = (TD / f"{name}.md").read_text(encoding="utf-8")
        dump(parse(md), TD / f"{name}.expected.json")

    # merge fixture：deepseek 只留一个会与源行匹配的模型 + 一个源中不存在的
    # 旧模型（覆盖 updated/added/missingInSource 三分支）；xai 原样作未动子树。
    real = json.loads((ROOT / "data" / "models.json").read_text(encoding="utf-8"))
    ds_rows = json.loads((TD / "ds.expected.json").read_text(encoding="utf-8"))
    first_id = ds_rows[0]["id"]
    provs = {p["id"]: p for p in real["providers"]}
    ds = copy.deepcopy(provs["deepseek"])
    ds["models"] = [m for m in ds["models"] if m["id"] == first_id]
    ds["models"].append({
        "id": "deepseek-legacy", "name": "DeepSeek Legacy",
        "contextWindow": 64000, "status": "active",
        "fetchedAt": "2026-01-01", "officialUpdatedAt": None,
        "tiers": [{"upToTokens": None, "input": 1.0, "output": 2.0}],
        "notes": "fixture 用：源中已消失的旧模型",
    })
    merge_input = {"meta": real["meta"],
                   "providers": [ds, provs["xai"]]}
    # 输入按 canonical 键序写盘——代表升级后稳态（首次写回已完成键序重排）。
    # Go 对未动 provider 原字节直通，Python 写回 canonical 重排；只有输入
    # 本身 canonical，两路输出才逐字节一致。
    merge_input = fetcher.canonicalize(merge_input)
    dump(merge_input, TD / "merge_input.json")

    dataset = copy.deepcopy(merge_input)
    report = fetcher.merge_provider(dataset, "deepseek", copy.deepcopy(ds_rows))
    dump({"dataset": dataset, "report": report}, TD / "merge_expected.json")

    # Python 真实写回字节：server/fetcher.py 写回前 canonicalize + json.dump
    # (ensure_ascii=False, indent=2)——原样记录，供 Go 写回逐字节对拍。
    dump(fetcher.canonicalize(dataset), TD / "merge_expected_file.json")


if __name__ == "__main__":
    main()
