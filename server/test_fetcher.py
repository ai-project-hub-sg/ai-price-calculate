"""fetcher 胜出校验与合并回归测试。"""
import unittest

from server import fetcher
from server.fetcher import MIN_USEFUL_BODY, SOURCES, _validate_body, canonicalize


class TestValidateBodySizeGate(unittest.TestCase):
    """E1 回归：小 body 即使 200/可解析也不得胜出——否则厂商被移出待决
    清单后又被最终门槛丢弃，既不合并也不在 unreachable（三不管）。"""

    def test_parserless_source_small_body_rejected(self):
        """moonshot 无解析器场景：地区限制/反爬小页不得胜出（原 bug）。"""
        small = b"x" * (MIN_USEFUL_BODY - 100)
        self.assertIsNone(SOURCES["moonshot"]["parse"])
        self.assertFalse(_validate_body("moonshot", small))

    def test_parserless_source_large_body_accepted(self):
        large = b"x" * (MIN_USEFUL_BODY + 100)
        self.assertTrue(_validate_body("moonshot", large))

    def test_parsed_source_size_gate_precedes_parse(self):
        small = b"x" * (MIN_USEFUL_BODY - 100)
        self.assertFalse(_validate_body("deepseek", small))

    def test_parsed_source_large_unparseable_rejected(self):
        large_garbage = b"x" * (MIN_USEFUL_BODY + 2000)
        self.assertFalse(_validate_body("deepseek", large_garbage))

    def test_boundary_exactly_at_threshold_rejected(self):
        self.assertFalse(_validate_body("moonshot", b"x" * MIN_USEFUL_BODY))
        self.assertTrue(_validate_body("moonshot", b"x" * (MIN_USEFUL_BODY + 1)))


class TestUpdateMaxNodesLimit(unittest.TestCase):
    """CR-005 库层守卫：超硬上限 9999 在入口即拒绝（对齐 Go fetch.Update），
    不依赖 API 层拦截。"""

    def test_over_limit_raises(self):
        for n in (10000, 10**20):
            with self.subTest(max_nodes=n), \
                    self.assertRaises(ValueError):
                fetcher.update(["deepseek"], max_nodes=n,
                               log=lambda _m: None)


class TestCanonicalize(unittest.TestCase):
    """canonical 键序规范：与 Go fieldOrders 镜像、与 meta.fields 自我声明一致。
    双端写回逐字节一致的前提（Python 历史上保留插入顺序，而各解析器
    tier 键序异质——deepseek input/cacheRead/output vs xai input/output/cacheRead）。"""

    def test_tier_order_canonicalized(self):
        tier = {"cacheRead": 0.006, "input": 0.3, "output": 1.2, "upToTokens": None}
        self.assertEqual(list(canonicalize(tier).keys()),
                         ["upToTokens", "input", "output", "cacheRead"])

    def test_provider_and_model_order(self):
        model = {"tiers": [], "id": "m", "notes": "n", "status": "active"}
        self.assertEqual(list(canonicalize(model).keys()),
                         ["id", "status", "tiers", "notes"])
        prov = {"models": [], "id": "p", "notes": "x", "checkedAt": "d"}
        self.assertEqual(list(canonicalize(prov).keys()),
                         ["id", "checkedAt", "notes", "models"])

    def test_unknown_keys_sorted_at_end(self):
        tier = {"zz": 1, "input": 0.3, "aa": 2}
        self.assertEqual(list(canonicalize(tier).keys()), ["input", "aa", "zz"])

    def test_nested_and_list_order(self):
        obj = {"models": [{"tiers": [{"output": 1, "input": 2}], "id": "m"}]}
        out = canonicalize(obj)
        self.assertEqual(list(out["models"][0].keys()), ["id", "tiers"])
        self.assertEqual(list(out["models"][0]["tiers"][0].keys()), ["input", "output"])

    def test_content_invariant_and_idempotent(self):
        import json
        with open("data/models.json", encoding="utf-8") as f:
            d = json.load(f)
        c = canonicalize(d)
        self.assertEqual(json.dumps(c, sort_keys=True), json.dumps(d, sort_keys=True))
        self.assertEqual(json.dumps(c), json.dumps(canonicalize(c)))


if __name__ == "__main__":
    unittest.main()
