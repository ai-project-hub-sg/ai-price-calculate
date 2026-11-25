"""Offline integration and atomic-write tests for the Python backend.

Run with: python -B -m unittest server.test_security server.test_app -v
"""

from __future__ import annotations

import http.client
import json
import os
import secrets
import sys
import tempfile
import threading
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from unittest import mock

from server import app, fetcher, security


_MISSING = object()


def _markdown(pid: str) -> bytes:
    if pid == "xai":
        row = "| grok-test-4.7 (< 200k prompt tokens) | 500k | $2.00 | $0.50 | $6.00 |"
    else:
        row = r"| GLM-TEST | \$1.4 | \$0.26 | Limited-time Free | \$4.4 |"
    return (row + "\n" + "x" * 600).encode("utf-8")


class AppServerTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        self.data = root / "data"
        self.data.mkdir()
        self.public = root / "public"
        self.public.mkdir()
        (self.public / "index.html").write_text("test-static-page", encoding="utf-8")
        self.config_path = self.data / "config.json"

        paths = mock.patch.multiple(
            app,
            DATA=str(self.data),
            CONFIG_PATH=str(self.config_path),
            FX_CACHE=str(self.data / "fx-cache.json"),
            PUBLIC=str(self.public),
        )
        paths.start()
        self.addCleanup(paths.stop)

        # Generated per test; these are not deployment credentials.
        self._test_password = secrets.token_urlsafe(32)
        self.auth = security.AuthManager("unit-test-admin", self._test_password)
        self.server = app.AuthHTTPServer(
            ("127.0.0.1", 0), app.Handler, self.auth)
        self.thread = threading.Thread(
            target=self.server.serve_forever,
            kwargs={"poll_interval": 0.01},
            daemon=True,
        )
        self.thread.start()
        self.addCleanup(self.stop_server)

    def stop_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        with app._JOBS_LOCK:
            app._JOBS.clear()

    def request(self, method, path, body=_MISSING, *, raw=None, token=None,
                headers=None):
        connection = http.client.HTTPConnection(
            "127.0.0.1", self.server.server_address[1], timeout=5)
        request_headers = dict(headers or {})
        payload = None
        if body is not _MISSING:
            payload = json.dumps(body).encode("utf-8")
            request_headers.setdefault("Content-Type", "application/json")
        elif raw is not None:
            payload = raw
            request_headers.setdefault("Content-Type", "application/json")
        if token is not None:
            request_headers["Authorization"] = f"Bearer {token}"
        try:
            connection.request(method, path, body=payload, headers=request_headers)
            response = connection.getresponse()
            response_body = response.read()
            return response.status, response_body, dict(response.getheaders())
        finally:
            connection.close()

    def request_json(self, method, path, body=_MISSING, **kwargs):
        status, raw, headers = self.request(method, path, body, **kwargs)
        return status, json.loads(raw) if raw else None, headers

    def login(self):
        status, session, _ = self.request_json(
            "POST", "/api/auth/login",
            {"username": "unit-test-admin", "password": self._test_password},
        )
        self.assertEqual(status, 200)
        self.assertTrue(session["token"])
        return session["token"]

    def test_api_requires_bearer_but_options_and_static_are_public(self):
        cases = [
            ("GET", "/api/config", _MISSING),
            ("PUT", "/api/config", {}),
            ("POST", "/api/pricing/update", {}),
            ("DELETE", "/api/config", _MISSING),
            ("PATCH", "/api/nodes", _MISSING),
            ("TRACE", "/api/config", _MISSING),
        ]
        for method, path, body in cases:
            with self.subTest(method=method, path=path):
                status, payload, _ = self.request(method, path, body)
                self.assertEqual(status, 401)
                self.assertIn("error", json.loads(payload))

        for authorization in ("Basic abc", "Bearer", "Bearer one two"):
            with self.subTest(authorization=authorization):
                status, _, _ = self.request(
                    "GET", "/api/config", headers={"Authorization": authorization})
                self.assertEqual(status, 401)

        status, _, headers = self.request("OPTIONS", "/api/config")
        self.assertEqual(status, 204)
        self.assertEqual(headers["Access-Control-Allow-Origin"], "*")
        self.assertIn("Authorization", headers["Access-Control-Allow-Headers"])

        status, payload, headers = self.request("GET", "/")
        self.assertEqual(status, 200)
        self.assertEqual(payload, b"test-static-page")
        self.assertEqual(headers["Access-Control-Allow-Origin"], "*")

        sibling = self.public.with_name("public-private")
        sibling.mkdir()
        (sibling / "secret.txt").write_text("not-public", encoding="utf-8")
        status, _, _ = self.request("GET", "/../public-private/secret.txt")
        self.assertEqual(status, 404)

    def test_login_bearer_access_and_logout(self):
        status, payload, _ = self.request_json(
            "POST", "/api/auth/login",
            {"username": "unit-test-admin", "password": "wrong-test-password"},
        )
        self.assertEqual(status, 401)
        self.assertIn("error", payload)

        malformed = json.dumps({"username": "\ud800", "password": "wrong"}).encode()
        status, _, _ = self.request(
            "POST", "/api/auth/login", raw=malformed,
            headers={"Content-Type": "application/json"})
        self.assertEqual(status, 401)

        token = self.login()
        status, _, _ = self.request_json("GET", "/api/config", token=token)
        self.assertEqual(status, 200)

        status, _, _ = self.request_json("POST", "/api/auth/logout")
        self.assertEqual(status, 401)
        status, payload, _ = self.request_json(
            "POST", "/api/auth/logout", token=token)
        self.assertEqual(status, 200)
        self.assertEqual(payload, {"ok": True})
        status, _, _ = self.request_json("GET", "/api/config", token=token)
        self.assertEqual(status, 401)

    def test_login_rate_limit_returns_429(self):
        for _ in range(5):
            status, _, _ = self.request_json(
                "POST", "/api/auth/login",
                {"username": "unit-test-admin", "password": "wrong-test-password"},
            )
            self.assertEqual(status, 401)
        status, payload, _ = self.request_json(
            "POST", "/api/auth/login",
            {"username": "unit-test-admin", "password": self._test_password},
        )
        self.assertEqual(status, 429)
        self.assertIn("error", payload)

    def test_bad_json_and_non_objects_never_overwrite_config(self):
        original = b'{"marker":"original","proxySubscription":""}'
        self.config_path.write_bytes(original)
        token = self.login()
        for raw in (b"{", b"[]", b"null", b'"text"', b"\xff", b"{}"):
            with self.subTest(raw=raw):
                status, payload, _ = self.request_json(
                    "PUT", "/api/config", raw=raw, token=token)
                self.assertEqual(status, 400)
                self.assertIn("error", payload)
                self.assertEqual(self.config_path.read_bytes(), original)

        status, payload, _ = self.request_json(
            "PUT", "/api/config", token=token,
            headers={"Content-Length": str(app.MAX_BODY_BYTES + 1)})
        self.assertEqual(status, 413)
        self.assertIn("error", payload)
        self.assertEqual(self.config_path.read_bytes(), original)

    def test_subscription_url_limit_counts_utf8_bytes(self):
        prefix = "https://example.invalid/"
        accepted = prefix + "é" * 2000
        rejected = prefix + "é" * 2100
        self.assertEqual(app.validation.validate_proxy_url(accepted), accepted)
        with self.assertRaisesRegex(ValueError, "URL 格式错误"):
            app.validation.validate_proxy_url(rejected)

    def test_config_subscription_validation_and_empty_values(self):
        original = {"marker": "original", "proxySubscription": ""}
        self.config_path.write_text(json.dumps(original), encoding="utf-8")
        token = self.login()
        for value in ("not-a-url", "ftp://example.invalid/sub", None, 12,
                      "https://example.invalid/has space", "   ",
                      "https://example.invalid/%zz"):
            with self.subTest(value=value):
                status, payload, _ = self.request_json(
                    "PUT", "/api/config",
                    {"marker": "changed", "proxySubscription": value}, token=token)
                self.assertEqual(status, 400)
                self.assertIn("error", payload)
                self.assertEqual(json.loads(self.config_path.read_text()), original)

        url = "  https://example.invalid/sub?value=fake  "
        status, _, _ = self.request_json(
            "PUT", "/api/config",
            {"marker": "trimmed", "proxySubscription": url}, token=token)
        self.assertEqual(status, 200)
        saved = json.loads(self.config_path.read_text())
        self.assertEqual(saved["proxySubscription"], url.strip())

        for body in ({"marker": "missing"}, {"marker": "empty",
                                             "proxySubscription": ""}):
            status, _, _ = self.request_json(
                "PUT", "/api/config", body, token=token)
            self.assertEqual(status, 200)
            self.assertEqual(json.loads(self.config_path.read_text()), body)

    def test_concurrent_config_puts_all_succeed_with_valid_json(self):
        token = self.login()
        count = 12
        barrier = threading.Barrier(count)

        def put(index):
            barrier.wait(timeout=5)
            return self.request_json(
                "PUT", "/api/config",
                {"marker": f"config-{index}", "proxySubscription": "",
                 "nested": {"value": index}},
                token=token,
            )

        with ThreadPoolExecutor(max_workers=count) as executor:
            results = list(executor.map(put, range(count)))
        self.assertTrue(all(status == 200 for status, _, _ in results), results)
        saved = json.loads(self.config_path.read_text(encoding="utf-8"))
        self.assertIn(saved["marker"], {f"config-{i}" for i in range(count)})
        self.assertEqual(saved["nested"]["value"], int(saved["marker"].split("-")[1]))
        self.assertEqual(list(self.data.glob(".config-*.tmp")), [])

    def test_config_io_failures_return_json_500_and_preserve_original(self):
        original = b'{"marker":"original"}'
        self.config_path.write_bytes(original)
        token = self.login()

        with mock.patch.object(app.os, "replace", side_effect=OSError("replace failed")):
            status, payload, _ = self.request_json(
                "PUT", "/api/config", {"marker": "new"}, token=token)
        self.assertEqual(status, 500)
        self.assertIn("error", payload)
        self.assertEqual(self.config_path.read_bytes(), original)
        self.assertEqual(list(self.data.glob(".config-*.tmp")), [])

        with mock.patch.object(app.os, "makedirs", side_effect=OSError("mkdir failed")):
            status, payload, _ = self.request_json(
                "PUT", "/api/config", {"marker": "new"}, token=token)
        self.assertEqual(status, 500)
        self.assertIn("error", payload)
        self.assertEqual(self.config_path.read_bytes(), original)

    def test_nodes_test_validates_url_without_fetching_subscription(self):
        token = self.login()
        for bad_url in ("not-a-url", None, 42):
            with self.subTest(url=bad_url), \
                    mock.patch.object(app, "_run_nodes_test") as run:
                status, payload, _ = self.request_json(
                    "POST", "/api/nodes/test", {"url": bad_url}, token=token)
                self.assertEqual(status, 400)
                self.assertIn("error", payload)
                run.assert_not_called()

        called = threading.Event()

        def fake_run(*args):
            called.set()

        with mock.patch.object(app, "_run_nodes_test", side_effect=fake_run) as run, \
                mock.patch.object(app.nodepool, "cache_test_results") as cache:
            status, payload, _ = self.request_json(
                "POST", "/api/nodes/test",
                {"url": "https://example.invalid/sub?value=fake", "maxNodes": 3},
                token=token)
            self.assertEqual(status, 200)
            self.assertTrue(payload["jobId"])
            self.assertTrue(called.wait(timeout=5))
            cache.assert_not_called()
            self.assertEqual(len(run.call_args.args), 3)

    def test_job_input_validation_does_not_start_jobs(self):
        token = self.login()
        for path in ("/api/pricing/update", "/api/nodes/test"):
            for value in (-1, 1.5, "3", True):
                with self.subTest(path=path, maxNodes=value):
                    status, payload, _ = self.request_json(
                        "POST", path, {"maxNodes": value}, token=token)
                    self.assertEqual(status, 400)
                    self.assertIn("maxNodes", payload["error"])
        for value in ("xai", ["xai", 1], {}):
            with self.subTest(providers=value):
                status, payload, _ = self.request_json(
                    "POST", "/api/pricing/update", {"providers": value}, token=token)
                self.assertEqual(status, 400)
                self.assertIn("providers", payload["error"])
        with app._JOBS_LOCK:
            self.assertEqual(app._JOBS, {})

    def test_shutdown_rejects_running_job(self):
        token = self.login()
        started = threading.Event()
        release = threading.Event()

        def blocked_update(*args, **kwargs):
            started.set()
            release.wait(timeout=5)
            return {"offline": True}

        with mock.patch.object(app.fetcher, "update", side_effect=blocked_update):
            try:
                status, payload, _ = self.request_json(
                    "POST", "/api/pricing/update", {"providers": []}, token=token)
                self.assertEqual(status, 200)
                job_id = payload["jobId"]
                self.assertTrue(started.wait(timeout=5))
                status, payload, _ = self.request_json(
                    "POST", "/api/shutdown", token=token)
                self.assertEqual(status, 409)
                self.assertIn("error", payload)
                status, _, _ = self.request_json("GET", "/api/config", token=token)
                self.assertEqual(status, 200)
            finally:
                release.set()

            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                status, job, _ = self.request_json(
                    "GET", f"/api/jobs/{job_id}", token=token)
                if status == 200 and job["status"] == "done":
                    break
                time.sleep(0.01)
            else:
                self.fail("offline update job did not finish")

    def test_normal_shutdown_responds_before_server_stops(self):
        token = self.login()
        status, payload, _ = self.request_json(
            "POST", "/api/shutdown", token=token)
        self.assertEqual(status, 200)
        self.assertEqual(payload, {"ok": True, "shutdown": True})
        self.thread.join(timeout=5)
        self.assertFalse(self.thread.is_alive())

    def test_main_fails_clearly_when_credentials_are_missing(self):
        with mock.patch.object(
            security, "load_manager",
            side_effect=RuntimeError("AIPC_ADMIN_PASSWORD is required"),
        ), mock.patch.object(sys, "argv", ["server.app", "--port", "0"]):
            with self.assertRaises(SystemExit) as caught:
                app.main()
        self.assertIn("启动失败", str(caught.exception))
        self.assertIn("AIPC_ADMIN_PASSWORD", str(caught.exception))


    def test_fx_refresh_and_invalid_cache(self):
        token = self.login()
        cache = {
            "ts": time.time(),
            "data": {"rate": 7.2, "source": "cached", "fetchedAt": "today"},
        }
        (self.data / "fx-cache.json").write_text(json.dumps(cache), encoding="utf-8")
        status, payload, _ = self.request_json(
            "GET", "/api/fx", token=token)
        self.assertEqual(status, 200)
        self.assertTrue(payload["cached"])

        calls = []

        def fake_fetch(url, proxy_port, timeout):
            calls.append(url)
            body = json.dumps({"rates": {"CNY": 7.3},
                               "time_last_update_utc": "now"}).encode()
            return {"status": 200, "body": body}

        with mock.patch.object(app.nodepool, "fetch_url", side_effect=fake_fetch):
            status, payload, _ = self.request_json(
                "GET", "/api/fx?refresh=1", token=token)
            self.assertEqual(status, 200)
            self.assertEqual(payload["rate"], 7.3)
            self.assertFalse(payload["cached"])
            cache["data"]["rate"] = 0
            (self.data / "fx-cache.json").write_text(
                json.dumps(cache), encoding="utf-8")
            status, payload, _ = self.request_json(
                "GET", "/api/fx", token=token)
            self.assertEqual(status, 200)
            self.assertEqual(payload["rate"], 7.3)
        self.assertEqual(len(calls), 2)

        for rate in (None, True, "7.3", 0, -1, float("nan"), float("inf")):
            with self.subTest(rate=rate):
                self.assertFalse(app._valid_fx_data({"rate": rate}))
        self.assertTrue(app._valid_fx_data({"rate": 7.3}))


class FetcherAtomicWriteTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.models_path = self.directory / "models.json"
        self.original = {
            "providers": [
                {"id": "xai", "name": "xAI", "models": []},
                {"id": "zai", "name": "Z.AI", "models": []},
            ]
        }
        self.original_bytes = json.dumps(
            self.original, ensure_ascii=False, indent=2).encode("utf-8")
        self.models_path.write_bytes(self.original_bytes)
        patcher = mock.patch.object(fetcher, "MODELS_PATH", str(self.models_path))
        patcher.start()
        self.addCleanup(patcher.stop)

    def assert_no_temporary_files(self):
        self.assertEqual(list(self.directory.glob("*.tmp")), [])
        self.assertEqual(list(self.directory.glob(".*.tmp")), [])

    def test_concurrent_updates_are_serialized_and_keep_both_merges(self):
        barrier = threading.Barrier(2)
        state_lock = threading.Lock()
        active = 0
        max_active = 0

        def fake_fetch(url, proxy_port, timeout):
            nonlocal active, max_active
            pid = "xai" if "docs.x.ai" in url else "zai"
            with state_lock:
                active += 1
                max_active = max(max_active, active)
            try:
                time.sleep(0.05)
                body = _markdown(pid)
                return {"status": 200, "bytes": len(body), "body": body}
            finally:
                with state_lock:
                    active -= 1

        def update(pid):
            barrier.wait(timeout=5)
            return fetcher.update([pid], log=lambda _message: None)

        with mock.patch.object(fetcher.nodepool, "fetch_url", side_effect=fake_fetch), \
                mock.patch.object(fetcher.nodepool, "find_working_proxy") as proxy:
            with ThreadPoolExecutor(max_workers=2) as executor:
                reports = list(executor.map(update, ("xai", "zai")))
        proxy.assert_not_called()
        self.assertEqual(max_active, 1)
        self.assertTrue(all(report["changed"] for report in reports))
        dataset = json.loads(self.models_path.read_text(encoding="utf-8"))
        self.assertEqual(len(dataset["providers"][0]["models"]), 1)
        self.assertEqual(len(dataset["providers"][1]["models"]), 1)
        backup = json.loads((self.directory / "models.json.bak").read_text(
            encoding="utf-8"))
        self.assertEqual(sum(bool(provider["models"])
                             for provider in backup["providers"]), 1)
        self.assert_no_temporary_files()

    def test_backup_failure_aborts_models_write_and_cleans_temp(self):
        def fake_fetch(url, proxy_port, timeout):
            body = _markdown("xai")
            return {"status": 200, "bytes": len(body), "body": body}

        with mock.patch.object(fetcher.nodepool, "fetch_url", side_effect=fake_fetch), \
                mock.patch.object(fetcher.os, "replace",
                                  side_effect=OSError("backup replace failed")):
            with self.assertRaisesRegex(OSError, "backup replace failed"):
                fetcher.update(["xai"], log=lambda _message: None)
        self.assertEqual(self.models_path.read_bytes(), self.original_bytes)
        self.assertFalse((self.directory / "models.json.bak").exists())
        self.assert_no_temporary_files()

    def test_models_replace_failure_keeps_original_and_atomic_backup(self):
        def fake_fetch(url, proxy_port, timeout):
            body = _markdown("xai")
            return {"status": 200, "bytes": len(body), "body": body}

        real_replace = os.replace
        calls = 0

        def fail_models_replace(source, destination):
            nonlocal calls
            calls += 1
            if calls == 2:
                raise OSError("models replace failed")
            return real_replace(source, destination)

        with mock.patch.object(fetcher.nodepool, "fetch_url", side_effect=fake_fetch), \
                mock.patch.object(fetcher.os, "replace",
                                  side_effect=fail_models_replace):
            with self.assertRaisesRegex(OSError, "models replace failed"):
                fetcher.update(["xai"], log=lambda _message: None)
        self.assertEqual(calls, 2)
        self.assertEqual(self.models_path.read_bytes(), self.original_bytes)
        self.assertEqual((self.directory / "models.json.bak").read_bytes(),
                         self.original_bytes)
        self.assert_no_temporary_files()


if __name__ == "__main__":
    unittest.main()
