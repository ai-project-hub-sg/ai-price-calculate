"""Standard-library administrator authentication for the Python server."""

from __future__ import annotations

import hashlib
import hmac
import os
import secrets
import sys
import threading
import time
from dataclasses import dataclass, field
from typing import Optional

ADMIN_USERNAME_ENV = "AIPC_ADMIN_USERNAME"
ADMIN_PASSWORD_ENV = "AIPC_ADMIN_PASSWORD"
WINDOWS_CREDENTIAL_TARGET = "aipc-server/admin"
CRED_TYPE_GENERIC = 1

_PASSWORD_SALT_BYTES = 32
_PASSWORD_ITERATIONS = 120_000
_TOKEN_BYTES = 32
_SESSION_TTL_SECONDS = 12 * 60 * 60
_RATE_LIMIT_WINDOW_SECONDS = 15 * 60
_MAX_FAILED_LOGINS = 5


class InvalidCredentials(Exception):
    """The supplied administrator username or password is invalid."""


class RateLimited(Exception):
    """The client has made too many failed login attempts recently."""


@dataclass(frozen=True)
class Credentials:
    username: str
    password: str = field(repr=False)


@dataclass(frozen=True)
class _Session:
    username: str
    expires_at: int


def _validate_credentials(credentials: Credentials) -> Credentials:
    if not credentials.username:
        raise RuntimeError(f"{ADMIN_USERNAME_ENV} is required")
    if not credentials.password:
        raise RuntimeError(f"{ADMIN_PASSWORD_ENV} is required")
    return credentials


def load_credentials() -> Credentials:
    """Load the administrator account without retaining a default password."""
    username = os.environ.get(ADMIN_USERNAME_ENV)
    password = os.environ.get(ADMIN_PASSWORD_ENV)

    if sys.platform == "win32":
        # A partially configured environment is an error, not a reason to
        # silently switch to a different administrator account.
        if username is not None or password is not None:
            if username is None or password is None:
                raise RuntimeError(
                    f"{ADMIN_USERNAME_ENV} and {ADMIN_PASSWORD_ENV} must both be set"
                )
            return _validate_credentials(Credentials(username, password))
        return _read_windows_credentials()

    return _validate_credentials(Credentials(username or "", password or ""))


def _read_windows_credentials() -> Credentials:
    # Importing ctypes and resolving Advapi32 are deliberately deferred so this
    # module remains importable on non-Windows systems.
    try:
        import ctypes
        from ctypes import wintypes
    except (ImportError, OSError) as exc:
        raise RuntimeError(
            "Windows Credential Manager is unavailable; set "
            f"{ADMIN_USERNAME_ENV} and {ADMIN_PASSWORD_ENV}"
        ) from exc

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

    try:
        advapi32 = ctypes.WinDLL("advapi32.dll", use_last_error=True)
        advapi32.CredReadW.argtypes = [
            wintypes.LPCWSTR,
            wintypes.DWORD,
            wintypes.DWORD,
            ctypes.POINTER(ctypes.POINTER(CredentialW)),
        ]
        advapi32.CredReadW.restype = wintypes.BOOL
        advapi32.CredFree.argtypes = [ctypes.c_void_p]
        advapi32.CredFree.restype = None
    except (AttributeError, OSError) as exc:
        raise RuntimeError(
            "Windows Credential Manager is unavailable; set "
            f"{ADMIN_USERNAME_ENV} and {ADMIN_PASSWORD_ENV}"
        ) from exc

    target = ctypes.create_unicode_buffer(WINDOWS_CREDENTIAL_TARGET)
    credential = ctypes.POINTER(CredentialW)()
    try:
        if not advapi32.CredReadW(
            target, CRED_TYPE_GENERIC, 0, ctypes.byref(credential)
        ):
            error_code = ctypes.get_last_error()
            raise RuntimeError(
                f"Windows credential {WINDOWS_CREDENTIAL_TARGET!r} is unavailable; "
                f"set {ADMIN_USERNAME_ENV} and {ADMIN_PASSWORD_ENV} "
                f"or configure that credential (Windows error {error_code})"
            )

        if not credential or not credential.contents.UserName:
            raise RuntimeError(f"Windows credential {WINDOWS_CREDENTIAL_TARGET!r} has no username")
        blob_size = credential.contents.CredentialBlobSize
        if blob_size == 0 or not credential.contents.CredentialBlob:
            raise RuntimeError(f"Windows credential {WINDOWS_CREDENTIAL_TARGET!r} has no password")
        if blob_size % 2:
            raise RuntimeError(
                f"Windows credential {WINDOWS_CREDENTIAL_TARGET!r} has an invalid password encoding"
            )

        # Copy both fields before finally releases the CREDENTIALW allocation.
        blob = ctypes.string_at(credential.contents.CredentialBlob, blob_size)
        username = ctypes.wstring_at(credential.contents.UserName)
        return _validate_credentials(
            Credentials(username, blob.decode("utf-16-le", errors="replace"))
        )
    finally:
        # CredReadW owns the returned allocation even when its fields are bad.
        if credential:
            advapi32.CredFree(credential)


def _hash_password(salt: bytes, password: str) -> bytes:
    return hashlib.pbkdf2_hmac(
        "sha256", password.encode("utf-8"), salt, _PASSWORD_ITERATIONS, dklen=32
    )


def _token_digest(token: str) -> bytes:
    return hashlib.sha256(token.encode("utf-8")).digest()


class AuthManager:
    """Thread-safe in-memory sessions backed by one administrator verifier."""

    def __init__(self, username: str, password: str):
        self._lock = threading.Lock()
        self._username = username if isinstance(username, str) else ""
        self._salt = secrets.token_bytes(_PASSWORD_SALT_BYTES)
        self._usable = bool(
            isinstance(username, str)
            and username
            and isinstance(password, str)
            and password
        )
        self._password_hash = (
            _hash_password(self._salt, password) if self._usable else b""
        )
        self._sessions: dict[bytes, _Session] = {}
        self._failures: dict[str, list[float]] = {}

    def login(self, username: str, password: str, client_key: str) -> dict:
        with self._lock:
            now = time.time()
            self._cleanup_expired_locked(now)
            if len(self._failures.get(client_key, [])) >= _MAX_FAILED_LOGINS:
                raise RateLimited("too many failed login attempts")

            username_matches = False
            password_matches = False
            try:
                username_matches = isinstance(username, str) and hmac.compare_digest(
                    username.encode("utf-8"), self._username.encode("utf-8")
                )
                if isinstance(password, str):
                    password_hash = _hash_password(self._salt, password)
                    password_matches = hmac.compare_digest(
                        password_hash, self._password_hash
                    )
            except UnicodeError:
                pass

            if not self._usable or not username_matches or not password_matches:
                self._failures.setdefault(client_key, []).append(now)
                raise InvalidCredentials("invalid credentials")

            self._failures.pop(client_key, None)
            token = secrets.token_urlsafe(_TOKEN_BYTES)
            expires_at = int(now + _SESSION_TTL_SECONDS)
            self._sessions[_token_digest(token)] = _Session(
                self._username, expires_at
            )
            return {
                "token": token,
                "expiresAt": expires_at,
                "username": self._username,
            }

    def authenticate(self, token: str) -> Optional[dict]:
        if not isinstance(token, str) or not token:
            return None

        try:
            token_hash = _token_digest(token)
        except UnicodeError:
            return None
        with self._lock:
            now = time.time()
            self._cleanup_expired_locked(now)
            session = self._sessions.get(token_hash)
            if session is None or session.expires_at <= int(now):
                self._sessions.pop(token_hash, None)
                return None
            return {
                "username": session.username,
                "expiresAt": session.expires_at,
            }

    def logout(self, token: str) -> None:
        if not isinstance(token, str) or not token:
            return None

        try:
            token_hash = _token_digest(token)
        except UnicodeError:
            return None
        with self._lock:
            self._cleanup_expired_locked(time.time())
            self._sessions.pop(token_hash, None)
        return None

    def _cleanup_expired_locked(self, now: float) -> None:
        now_seconds = int(now)
        for token_hash, session in list(self._sessions.items()):
            if session.expires_at <= now_seconds:
                del self._sessions[token_hash]

        cutoff = now - _RATE_LIMIT_WINDOW_SECONDS
        for client_key, failures in list(self._failures.items()):
            current = [attempt for attempt in failures if attempt > cutoff]
            if current:
                self._failures[client_key] = current
            else:
                del self._failures[client_key]


def load_manager() -> AuthManager:
    credentials = load_credentials()
    return AuthManager(credentials.username, credentials.password)
