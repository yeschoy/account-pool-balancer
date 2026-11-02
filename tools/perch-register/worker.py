#!/usr/bin/env python3
"""Single-account Perch OAuth worker. Credentials only enter through stdin."""
from __future__ import annotations

import contextlib
import contextvars
import io
import json
import sys

sys.dont_write_bytecode = True

_stage = contextvars.ContextVar("worker_stage", default="internal_error")


def run(data):
    _stage.set("input_validation")
    if not isinstance(data, dict):
        raise ValueError("invalid input")
    fields = ("login", "password", "totp_secret", "mailbox_url")
    if any(not isinstance(data.get(field), str) or not data[field] for field in fields):
        raise ValueError("invalid input")
    proxy_value = data.get("proxy")
    if proxy_value is not None and (not isinstance(proxy_value, str) or not proxy_value):
        raise ValueError("invalid input")
    _stage.set("dependency_unavailable")
    from logicc_protocol import Pacer, ProxyConfig
    from open_design_github import GithubAccount
    from perch_export import SessionExtractor, SessionValidator
    from perch_login import RouteSession, SUPABASE_KEY, SUPABASE_URL, sign_in

    _stage.set("input_validation")
    account = GithubAccount(*(data[field] for field in fields))
    proxy = ProxyConfig(proxy_value) if proxy_value is not None else None
    _stage.set("github_oauth")
    pacer = Pacer(0.3, 0.3, log=lambda _: None)
    result = sign_in(account, proxy=proxy, pacer=pacer, log=lambda _: None)
    _stage.set("session_validation")
    result["login"] = account.login
    session = SessionExtractor.extract(result)
    route = RouteSession(proxy)
    try:
        validator = SessionValidator(supabase_url=SUPABASE_URL, supabase_key=SUPABASE_KEY,
                                     client_factory=route.client)
        verified = validator.validate(session)
    finally:
        route.close()
    document = SessionExtractor.to_document(verified, supabase_url=SUPABASE_URL,
                                             supabase_anon_key=SUPABASE_KEY)
    # A refresh can rotate both tokens and return a newer user object.
    full_session = dict(result["supabase_session"])
    full_session.update(verified.get("_refresh_payload", {}))
    if isinstance(verified.get("user"), dict):
        full_session["user"] = verified["user"]
    for key in ("access_token", "refresh_token", "expires_at"):
        full_session[key] = verified[key]
    document["supabase_session"] = full_session
    return document


def main():
    _stage.set("internal_error")
    try:
        data = json.load(sys.stdin)
    except (ValueError, UnicodeError):
        print("worker: input_validation", file=sys.stderr)
        return 2
    except Exception:
        print("worker: internal_error", file=sys.stderr)
        return 1
    try:
        # Legacy code and third-party libraries must never contaminate stdout or logs.
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            document = run(data)
        encoded = json.dumps(document, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
    except Exception:
        print(f"worker: {_stage.get()}", file=sys.stderr)
        return 1
    print(encoded)
    return 0


if __name__ == "__main__":
    sys.exit(main())
