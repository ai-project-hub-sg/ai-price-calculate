"""Tests for server.security; run with python -m unittest server.test_security."""

import base64
import builtins
import ctypes
import hashlib
import importlib.util
import os
import sys
import threading
import unittest
from concurrent.futures import ThreadPoolExecutor
from ctypes import wintypes
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

from server import security


PASSWORD = "correct horse battery staple"
USERNAME = "admin"


class CredentialLoadingTests(unittest.TestCase):
    def test_credentials_preserve_values_and_hide_password_repr(self):
        credentials = security.Credentials("管理员", "  secret with spaces  ")
        self.assertEqual(credentials.username, "管理员")
        self.assertEqual(credentials.password, "  secret with spaces  ")
        self.assertNotIn(credentials.password, repr(credentials))

    def test_linux_loads_credentials_from_environment_without_trimming(self):
        environment = {
            security.ADMIN_USERNAME_ENV: "admin",
            security.ADMIN_PASSWORD_ENV: "  secret with spaces  ",
        }
        with mock.patch.dict(os.environ, environment, clear=True), mock.patch.object(
            sys, "platform", "linux"
        ):
            self.assertEqual(
                security.load_credentials(),
                security.Credentials("admin", "  secret with spaces  "),
            )

    def test_missing_or_empty_environment_credentials_have_clear_errors(self):
        cases = [
            ({}, security.ADMIN_USERNAME_ENV),
            ({security.ADMIN_PASSWORD_ENV: "secret"}, security.ADMIN_USERNAME_ENV),
            ({security.ADMIN_USERNAME_ENV: "admin"}, security.ADMIN_PASSWORD_ENV),
            (
                {
                    security.ADMIN_USERNAME_ENV: "",
                    security.ADMIN_PASSWORD_ENV: "secret",
                },
                security.ADMIN_USERNAME_ENV,
            ),
            (
                {
                    security.ADMIN_USERNAME_ENV: "admin",
                    security.ADMIN_PASSWORD_ENV: "",
                },
                security.ADMIN_PASSWORD_ENV,
            ),
        ]
        for environment, missing_name in cases:
            with self.subTest(environment=environment), mock.patch.dict(
                os.environ, environment, clear=True
            ), mock.patch.object(sys, "platform", "linux"):
                with self.assertRaisesRegex(RuntimeError, missing_name) as caught:
                    security.load_credentials()
                self.assertNotIn("secret", str(caught.exception))

    def test_windows_prefers_complete_environment(self):
        environment = {
            security.ADMIN_USERNAME_ENV: "env-admin",
            security.ADMIN_PASSWORD_ENV: "env-secret",
        }
        with mock.patch.dict(os.environ, environment, clear=True), mock.patch.object(
            sys, "platform", "win32"
        ), mock.patch.object(
            security, "_read_windows_credentials"
        ) as read_windows:
            self.assertEqual(
                security.load_credentials(),
                security.Credentials("env-admin", "env-secret"),
            )
            read_windows.assert_not_called()

    def test_windows_rejects_partial_environment_instead_of_falling_back(self):
        cases = [
            ({security.ADMIN_USERNAME_ENV: "admin"}, None),
            ({security.ADMIN_PASSWORD_ENV: "secret"}, None),
            (
                {
                    security.ADMIN_USERNAME_ENV: "",
                    security.ADMIN_PASSWORD_ENV: "secret",
                },
                security.ADMIN_USERNAME_ENV,
            ),
            (
                {
                    security.ADMIN_USERNAME_ENV: "admin",
                    security.ADMIN_PASSWORD_ENV: "",
                },
                security.ADMIN_PASSWORD_ENV,
            ),
        ]
        for environment, empty_name in cases:
            with self.subTest(environment=environment), mock.patch.dict(
                os.environ, environment, clear=True
            ), mock.patch.object(sys, "platform", "win32"), mock.patch.object(
                security, "_read_windows_credentials"
            ) as read_windows:
                with self.assertRaises(RuntimeError) as caught:
                    security.load_credentials()
                message = str(caught.exception)
                if empty_name is None:
                    self.assertIn(security.ADMIN_USERNAME_ENV, message)
                    self.assertIn(security.ADMIN_PASSWORD_ENV, message)
                else:
                    self.assertIn(empty_name, message)
                self.assertNotIn("secret", message)
                read_windows.assert_not_called()

    def test_windows_uses_credential_manager_when_environment_is_absent(self):
        with mock.patch.dict(os.environ, {}, clear=True), mock.patch.object(
            sys, "platform", "win32"
        ), mock.patch.object(
            security,
            "_read_windows_credentials",
            return_value=security.Credentials("stored-admin", "stored-secret"),
        ) as read_windows:
            self.assertEqual(
                security.load_credentials(),
                security.Credentials("stored-admin", "stored-secret"),
            )
            read_windows.assert_called_once_with()

    def test_load_manager_uses_loaded_credentials(self):
        with mock.patch.object(
            security,
            "load_credentials",
            return_value=security.Credentials(USERNAME, PASSWORD),
        ):
            manager = security.load_manager()
        session = manager.login(USERNAME, PASSWORD, "client")
        self.assertEqual(session["username"], USERNAME)

    def test_load_manager_propagates_missing_credentials(self):
        with mock.patch.object(
            security,
            "load_credentials",
            side_effect=RuntimeError("AIPC_ADMIN_PASSWORD is required"),
        ):
            with self.assertRaisesRegex(RuntimeError, "AIPC_ADMIN_PASSWORD"):
                security.load_manager()

    def test_module_import_does_not_load_ctypes_on_either_platform(self):
        module_name = "_security_import_without_ctypes"
        source = Path(security.__file__)
        original_import = builtins.__import__

        def guarded_import(name, *args, **kwargs):
            if name == "ctypes" or name.startswith("ctypes."):
                raise AssertionError("ctypes was imported during module import")
            return original_import(name, *args, **kwargs)

        for platform in ("linux", "win32"):
            with self.subTest(platform=platform):
                spec = importlib.util.spec_from_file_location(module_name, source)
                module = importlib.util.module_from_spec(spec)
                with mock.patch.dict(sys.modules, {module_name: module}), mock.patch.object(
                    sys, "platform", platform
                ), mock.patch("builtins.__import__", side_effect=guarded_import):
                    spec.loader.exec_module(module)
                self.assertFalse(hasattr(module, "ctypes"))


class WindowsCredentialManagerTests(unittest.TestCase):
    class CredentialW(ctypes.Structure):
        _fields_ = [
            ("Flags", wintypes.DWORD),
            ("Type", wintypes.DWORD),
            ("TargetName", wintypes.LPCWSTR),
            ("Comment", wintypes.LPCWSTR),
            ("LastWritten", wintypes.FILETIME),
            ("CredentialBlobSize", wintypes.DWORD),
            ("CredentialBlob", wintypes.LPBYTE),
            ("Persist", wintypes.DWORD),
            ("AttributeCount", wintypes.DWORD),
            ("Attributes", wintypes.LPBYTE),
            ("TargetAlias", wintypes.LPCWSTR),
            ("UserName", wintypes.LPCWSTR),
        ]

    def make_advapi32(
        self,
        *,
        username="管理员",
        password="密码 with spaces",
        blob=None,
        blob_size=None,
        succeeds=True,
    ):
        references = []
        allocated = self.CredentialW()
        address = ctypes.addressof(allocated)
        references.append(allocated)

        if username is not None:
            username_buffer = ctypes.create_unicode_buffer(username)
            references.append(username_buffer)
            allocated.UserName = ctypes.cast(username_buffer, wintypes.LPCWSTR)

        if blob is None and password is not None:
            blob = password.encode("utf-16-le")
        if blob is not None:
            blob_buffer = ctypes.create_string_buffer(blob)
            references.append(blob_buffer)
            allocated.CredentialBlob = ctypes.cast(blob_buffer, wintypes.LPBYTE)
        allocated.CredentialBlobSize = (
            len(blob) if blob_size is None and blob is not None else blob_size or 0
        )

        def cred_read(target, credential_type, flags, output):
            self.assertEqual(
                ctypes.cast(target, wintypes.LPCWSTR).value,
                security.WINDOWS_CREDENTIAL_TARGET,
            )
            self.assertEqual(credential_type, security.CRED_TYPE_GENERIC)
            self.assertEqual(flags, 0)
            if succeeds:
                pointer = ctypes.cast(ctypes.pointer(allocated), ctypes.POINTER(self.CredentialW))
                output_pointer = ctypes.cast(
                    output, ctypes.POINTER(ctypes.POINTER(self.CredentialW))
                )
                output_pointer[0] = pointer
            return int(succeeds)

        read = mock.Mock(side_effect=cred_read)

        def cred_free(_pointer):
            # Simulate the allocator reclaiming everything as soon as CredFree
            # runs, so reading a field after the free would fail the test.
            for reference in references:
                ctypes.memset(ctypes.addressof(reference), 0, ctypes.sizeof(reference))

        free = mock.Mock(side_effect=cred_free)
        library = SimpleNamespace(CredReadW=read, CredFree=free)
        return library, read, free, address, references

    def test_reads_unicode_credentials_and_frees_native_allocation(self):
        library, read, free, address, references = self.make_advapi32()
        with mock.patch.object(ctypes, "WinDLL", return_value=library, create=True):
            credentials = security._read_windows_credentials()
        self.assertEqual(credentials, security.Credentials("管理员", "密码 with spaces"))
        read.assert_called_once()
        free.assert_called_once()
        self.assertEqual(ctypes.cast(free.call_args.args[0], ctypes.c_void_p).value, address)
        self.assertEqual(read.restype, wintypes.BOOL)
        self.assertIsNone(free.restype)
        self.assertTrue(references)

    def test_missing_username_still_frees_native_allocation(self):
        library, _, free, address, _ = self.make_advapi32(username=None)
        with mock.patch.object(ctypes, "WinDLL", return_value=library, create=True):
            with self.assertRaisesRegex(RuntimeError, "has no username"):
                security._read_windows_credentials()
        free.assert_called_once()
        self.assertEqual(ctypes.cast(free.call_args.args[0], ctypes.c_void_p).value, address)

    def test_missing_or_odd_sized_blob_frees_native_allocation(self):
        cases = [
            ({"password": None}, "has no password"),
            ({"password": "x", "blob_size": 1}, "invalid password encoding"),
        ]
        for options, message in cases:
            with self.subTest(options=options):
                library, _, free, address, _ = self.make_advapi32(**options)
                with mock.patch.object(ctypes, "WinDLL", return_value=library, create=True):
                    with self.assertRaisesRegex(RuntimeError, message):
                        security._read_windows_credentials()
                free.assert_called_once()
                self.assertEqual(ctypes.cast(free.call_args.args[0], ctypes.c_void_p).value, address)

    def test_failed_read_reports_setup_options_without_freeing_null_pointer(self):
        library, read, free, _, _ = self.make_advapi32(succeeds=False)
        with mock.patch.object(ctypes, "WinDLL", return_value=library, create=True), mock.patch.object(
            ctypes, "get_last_error", return_value=1168, create=True
        ):
            with self.assertRaises(RuntimeError) as caught:
                security._read_windows_credentials()
        message = str(caught.exception)
        self.assertIn(security.WINDOWS_CREDENTIAL_TARGET, message)
        self.assertIn(security.ADMIN_USERNAME_ENV, message)
        self.assertIn(security.ADMIN_PASSWORD_ENV, message)
        self.assertIn("1168", message)
        read.assert_called_once()
        free.assert_not_called()

    def test_unavailable_advapi32_has_clear_error(self):
        with mock.patch.object(
            ctypes, "WinDLL", side_effect=OSError("not available"), create=True
        ):
            with self.assertRaisesRegex(RuntimeError, "AIPC_ADMIN_USERNAME"):
                security._read_windows_credentials()


class AuthManagerTests(unittest.TestCase):
    def make_manager(self):
        manager = security.AuthManager(USERNAME, PASSWORD)
        self.addCleanup(self._assert_password_not_retained, manager)
        return manager

    def _assert_password_not_retained(self, manager):
        self.assertNotIn(PASSWORD, repr(manager.__dict__))

    def test_login_authenticate_contract_and_token_storage(self):
        manager = self.make_manager()
        now = 1_700_000_000.75
        with mock.patch.object(security.time, "time", return_value=now):
            session = manager.login(USERNAME, PASSWORD, "client-a")
            self.assertEqual(
                set(session), {"token", "expiresAt", "username"}
            )
            self.assertEqual(session["username"], USERNAME)
            self.assertEqual(session["expiresAt"], int(now + 12 * 60 * 60))
            self.assertEqual(len(base64.urlsafe_b64decode(session["token"] + "=")), 32)
            self.assertNotIn("=", session["token"])
            self.assertEqual(
                manager.authenticate(session["token"]),
                {"username": USERNAME, "expiresAt": session["expiresAt"]},
            )
            claims = manager.authenticate(session["token"])
            claims["username"] = "changed"
            self.assertEqual(manager.authenticate(session["token"])["username"], USERNAME)

        digest = hashlib.sha256(session["token"].encode("utf-8")).digest()
        self.assertIn(digest, manager._sessions)
        self.assertNotIn(session["token"], repr(manager._sessions))

    def test_password_uses_random_salted_pbkdf2_verifier(self):
        first = self.make_manager()
        second = self.make_manager()
        self.assertEqual(len(first._salt), 32)
        self.assertNotEqual(first._salt, second._salt)
        self.assertNotEqual(first._password_hash, second._password_hash)
        expected = hashlib.pbkdf2_hmac(
            "sha256", PASSWORD.encode("utf-8"), first._salt, 120_000, dklen=32
        )
        self.assertEqual(first._password_hash, expected)

    def test_wrong_and_empty_credentials_are_rejected_without_leaking_secrets(self):
        manager = self.make_manager()
        cases = [
            (USERNAME, "wrong"),
            ("other", PASSWORD),
            ("other", "wrong"),
            ("", ""),
            (None, None),
            ("\ud800", PASSWORD),
            (USERNAME, "\ud800"),
        ]
        for index, (username, password) in enumerate(cases):
            with self.subTest(username=username, password=password):
                with self.assertRaises(security.InvalidCredentials) as caught:
                    manager.login(username, password, f"client-{index}")
                self.assertEqual(str(caught.exception), "invalid credentials")
                self.assertNotIn(PASSWORD, str(caught.exception))
                self.assertNotIn("wrong", str(caught.exception))
        self.assertEqual(manager._sessions, {})

    def test_incomplete_manager_fails_closed(self):
        cases = [("", ""), (USERNAME, ""), ("", PASSWORD), (None, None)]
        for username, password in cases:
            with self.subTest(username=username, password=password):
                manager = security.AuthManager(username, password)
                with self.assertRaises(security.InvalidCredentials):
                    manager.login(username, password, "client")

    def test_authenticate_treats_token_as_opaque(self):
        manager = self.make_manager()
        session = manager.login(USERNAME, PASSWORD, "client")
        self.assertIsNone(manager.authenticate("Bearer " + session["token"]))
        self.assertIsNone(manager.authenticate("unknown"))
        self.assertIsNone(manager.authenticate(""))
        self.assertIsNone(manager.authenticate(None))
        self.assertIsNone(manager.authenticate("\ud800"))
        manager.logout("\ud800")
        self.assertIsNotNone(manager.authenticate(session["token"]))

    def test_expired_sessions_are_rejected_at_exact_boundary_and_cleaned_up(self):
        manager = self.make_manager()
        now = [1_700_000_000.0]
        with mock.patch.object(security.time, "time", side_effect=lambda: now[0]):
            first = manager.login(USERNAME, PASSWORD, "client-a")
            now[0] += 60 * 60
            second = manager.login(USERNAME, PASSWORD, "client-b")
            now[0] += 11 * 60 * 60
            self.assertIsNone(manager.authenticate(first["token"]))
            self.assertEqual(len(manager._sessions), 1)
            self.assertIsNotNone(manager.authenticate(second["token"]))
            now[0] += 60 * 60
            self.assertIsNone(manager.authenticate(second["token"]))
            self.assertEqual(manager._sessions, {})

    def test_expired_cleanup_also_runs_on_login_and_logout(self):
        for operation in ("login", "logout"):
            with self.subTest(operation=operation):
                manager = self.make_manager()
                now = [1_700_000_000.0]
                with mock.patch.object(security.time, "time", side_effect=lambda: now[0]):
                    session = manager.login(USERNAME, PASSWORD, "client-a")
                    now[0] += 12 * 60 * 60
                    if operation == "login":
                        manager.login(USERNAME, PASSWORD, "client-b")
                    else:
                        manager.logout("unknown")
                    self.assertNotIn(
                        hashlib.sha256(session["token"].encode()).digest(),
                        manager._sessions,
                    )

    def test_logout_invalidates_only_its_token_and_is_idempotent(self):
        manager = self.make_manager()
        first = manager.login(USERNAME, PASSWORD, "client-a")
        second = manager.login(USERNAME, PASSWORD, "client-b")
        self.assertNotEqual(first["token"], second["token"])

        manager.logout("Bearer " + first["token"])
        self.assertIsNotNone(manager.authenticate(first["token"]))
        manager.logout(first["token"])
        self.assertIsNone(manager.authenticate(first["token"]))
        self.assertIsNotNone(manager.authenticate(second["token"]))
        manager.logout(first["token"])
        manager.logout("unknown")
        manager.logout("")
        manager.logout(None)
        self.assertIsNotNone(manager.authenticate(second["token"]))
        manager.logout(second["token"])
        self.assertEqual(manager._sessions, {})

    def test_rate_limit_is_per_client_and_starts_after_five_failures(self):
        manager = self.make_manager()
        for _ in range(5):
            with self.assertRaises(security.InvalidCredentials):
                manager.login(USERNAME, "wrong", "client-a")
        with self.assertRaises(security.RateLimited):
            manager.login(USERNAME, PASSWORD, "client-a")
        self.assertIsNotNone(manager.login(USERNAME, PASSWORD, "client-b"))

    def test_successful_login_resets_only_that_clients_failures(self):
        manager = self.make_manager()
        for _ in range(4):
            with self.assertRaises(security.InvalidCredentials):
                manager.login(USERNAME, "wrong", "client-a")
        manager.login(USERNAME, PASSWORD, "client-a")
        self.assertNotIn("client-a", manager._failures)

        with self.assertRaises(security.InvalidCredentials):
            manager.login(USERNAME, "wrong", "client-b")
        for _ in range(5):
            with self.assertRaises(security.InvalidCredentials):
                manager.login(USERNAME, "wrong", "client-a")
        with self.assertRaises(security.RateLimited):
            manager.login(USERNAME, PASSWORD, "client-a")
        self.assertIn("client-b", manager._failures)

    def test_rate_limit_window_expires_at_exact_boundary(self):
        manager = self.make_manager()
        now = [1_700_000_000.0]
        with mock.patch.object(security.time, "time", side_effect=lambda: now[0]):
            for _ in range(5):
                with self.assertRaises(security.InvalidCredentials):
                    manager.login(USERNAME, "wrong", "client-a")
            now[0] += 15 * 60
            session = manager.login(USERNAME, PASSWORD, "client-a")
            self.assertIsNotNone(manager.authenticate(session["token"]))
            self.assertNotIn("client-a", manager._failures)

    def test_cleanup_removes_other_clients_expired_failures(self):
        manager = self.make_manager()
        now = [1_700_000_000.0]
        with mock.patch.object(security.time, "time", side_effect=lambda: now[0]):
            for client in ("client-a", "client-b"):
                with self.assertRaises(security.InvalidCredentials):
                    manager.login(USERNAME, "wrong", client)
            now[0] += 15 * 60
            manager.login(USERNAME, PASSWORD, "client-c")
            self.assertEqual(manager._failures, {})

    def test_concurrent_logins_and_logouts_are_thread_safe(self):
        manager = self.make_manager()
        count = 8
        barrier = threading.Barrier(count)

        def login(_):
            barrier.wait(timeout=5)
            return manager.login(USERNAME, PASSWORD, "shared-client")

        with ThreadPoolExecutor(max_workers=count) as executor:
            sessions = list(executor.map(login, range(count)))
        self.assertEqual(len(manager._sessions), count)
        self.assertEqual(len({session["token"] for session in sessions}), count)
        for session in sessions:
            self.assertIsNotNone(manager.authenticate(session["token"]))

        with ThreadPoolExecutor(max_workers=count) as executor:
            list(executor.map(lambda session: manager.logout(session["token"]), sessions))
        self.assertEqual(manager._sessions, {})

    def test_concurrent_failures_cannot_exceed_the_limit(self):
        manager = self.make_manager()
        count = 10
        barrier = threading.Barrier(count)

        def fail(_):
            barrier.wait(timeout=5)
            try:
                manager.login(USERNAME, "wrong", "shared-client")
            except security.InvalidCredentials:
                return "invalid"
            except security.RateLimited:
                return "limited"
            self.fail("wrong password was accepted")

        with ThreadPoolExecutor(max_workers=count) as executor:
            results = list(executor.map(fail, range(count)))
        self.assertEqual(results.count("invalid"), 5)
        self.assertEqual(results.count("limited"), 5)
        self.assertEqual(len(manager._failures["shared-client"]), 5)


if __name__ == "__main__":
    unittest.main()
