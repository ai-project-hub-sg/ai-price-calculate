from __future__ import annotations

import re
from urllib.parse import urlsplit


def validate_proxy_url(value: object) -> str:
    if not isinstance(value, str):
        raise ValueError("代理订阅 URL 必须是字符串")
    raw = value.strip()
    if not raw:
        raise ValueError("代理订阅 URL 不能为空")
    if len(raw) > 4096 or any(ch.isspace() for ch in raw):
        raise ValueError("代理订阅 URL 格式错误")
    if re.search(r"%(?![0-9a-fA-F]{2})", raw):
        raise ValueError("代理订阅 URL 格式错误")
    try:
        parsed = urlsplit(raw)
        port = parsed.port
    except ValueError as exc:
        raise ValueError("代理订阅 URL 格式错误") from exc
    if parsed.scheme.lower() not in {"http", "https"} or not parsed.hostname:
        raise ValueError("代理订阅 URL 格式错误：仅支持完整的 http/https 地址")
    if port is not None and not 1 <= port <= 65535:
        raise ValueError("代理订阅 URL 格式错误：端口无效")
    return raw
