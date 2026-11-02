import contextlib
import io
import json
import os
import subprocess
import sys
import unittest
from unittest.mock import patch

import perch_export
import perch_login
import worker

INPUT = {"login": "example-user", "password": "secret-password", "totp_secret": "ABCDEFGHIJKLMNOP",
         "mailbox_url": "https://example.invalid/mail/secret", "proxy": "http://user:secret@localhost:8080"}
USER = {"id": "user-1", "email": "nobody@example.invalid", "app_metadata": {"provider": "github"}}


class Response:
    status_code = 200

    def __init__(self, payload):
        self.payload = payload

    def json(self):
        return self.payload


class Client:
    def __init__(self, refresh=None, user=None):
        self.refresh = refresh
        self.user = user or USER

    def post(self, *args, **kwargs):
        return Response(self.refresh)

    def get(self, *args, **kwargs):
        return Response(self.user)

    def close(self):
        pass


class FakeRoute:
    instances = []
    refresh = None
    user = USER

    def __init__(self, proxy):
        self.proxy = proxy
        self.closed = False
        self.instances.append(self)

    def client(self):
        return Client(self.refresh, self.user)

    def close(self):
        self.closed = True


def login_result(expires):
    return {"supabase_session": {"access_token": "old-access", "refresh_token": "old-refresh",
                                 "expires_at": expires, "user": USER, "token_type": "bearer"},
            "logged_in_at": "2026-01-01T00:00:00Z"}


class WorkerTests(unittest.TestCase):
    def setUp(self):
        FakeRoute.instances = []
        FakeRoute.refresh = None
        FakeRoute.user = USER

    def test_validated_output_with_rotated_session(self):
        FakeRoute.refresh = {"access_token": "new-access", "refresh_token": "new-refresh",
                             "expires_at": 9999999999, "expires_in": 3600,
                             "user": USER, "token_type": "new-type"}
        with patch.object(perch_login, "sign_in", return_value=login_result(1)) as login, \
                patch.object(perch_login, "RouteSession", FakeRoute):
            document = worker.run(INPUT)
        self.assertEqual(login.call_args.kwargs["proxy"].url, INPUT["proxy"])
        self.assertEqual(FakeRoute.instances[0].proxy.url, INPUT["proxy"])
        self.assertTrue(FakeRoute.instances[0].closed)
        self.assertEqual(document["account"]["supabase_user_id"], "user-1")
        self.assertEqual(document["session"], {"access_token": "new-access", "refresh_token": "new-refresh", "expires_at": 9999999999})
        self.assertEqual(document["supabase_session"]["access_token"], "new-access")
        self.assertEqual(document["supabase_session"]["refresh_token"], "new-refresh")
        self.assertEqual(document["supabase_session"]["token_type"], "new-type")
        self.assertNotIn(INPUT["password"], json.dumps(document))

    def test_identity_change_on_refresh_rejected(self):
        FakeRoute.refresh = {"access_token": "new", "refresh_token": "new-refresh", "expires_at": 9999999999,
                             "user": {"id": "other", "email": USER["email"]}}
        FakeRoute.user = {"id": "other", "email": USER["email"]}
        with patch.object(perch_login, "sign_in", return_value=login_result(1)), \
                patch.object(perch_login, "RouteSession", FakeRoute):
            with self.assertRaises(perch_export.SessionError):
                worker.run(INPUT)
        self.assertTrue(FakeRoute.instances[0].closed)

    def test_main_stdout_and_suppressed_logs(self):
        out, err = io.StringIO(), io.StringIO()
        def fake_run(data):
            print("secret-password")
            print("secret-password", file=sys.stderr)
            return {"session": {"access_token": "token"}}
        with patch.object(sys, "stdin", io.StringIO(json.dumps(INPUT))), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err), patch.object(worker, "run", fake_run):
            self.assertEqual(worker.main(), 0)
        self.assertEqual(json.loads(out.getvalue()), {"session": {"access_token": "token"}})
        self.assertEqual(err.getvalue(), "")

    def test_exception_does_not_leak(self):
        out, err = io.StringIO(), io.StringIO()
        with patch.object(sys, "stdin", io.StringIO(json.dumps(INPUT))), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err), \
                patch.object(worker, "run", side_effect=Exception(json.dumps(INPUT))):
            self.assertEqual(worker.main(), 1)
        self.assertEqual(out.getvalue(), "")
        self.assertNotIn("secret", err.getvalue())

    def test_bad_input_subprocess(self):
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
        for payload in ("not-json secret-password", "{}", "[]", json.dumps({**INPUT, "proxy": 4})):
            result = subprocess.run([sys.executable, "-B", worker.__file__], input=payload,
                                    text=True, capture_output=True, env=env, check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "")
            self.assertNotIn("secret", result.stderr)

    def test_main_failure_stages_are_fixed_and_do_not_leak(self):
        sensitive = json.dumps(INPUT) + ' access-token refresh-token upstream-response'

        def invoke(data=INPUT):
            out, err = io.StringIO(), io.StringIO()
            with patch.object(sys, "stdin", io.StringIO(json.dumps(data))), \
                    contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                code = worker.main()
            return code, out.getvalue(), err.getvalue()

        for data in ({}, [], {**INPUT, "login": "bad user"},
                     {**INPUT, "totp_secret": "invalid"}, {**INPUT, "proxy": "invalid"}):
            with self.subTest(data=data):
                self.assertEqual(invoke(data), (1, "", "worker: input_validation\n"))

        def fail_oauth(*args, **kwargs):
            print(sensitive)
            print(sensitive, file=sys.stderr)
            raise RuntimeError(sensitive)

        with patch.object(perch_login, "sign_in", side_effect=fail_oauth):
            self.assertEqual(invoke(), (1, "", "worker: github_oauth\n"))

        with patch.object(perch_login, "sign_in", return_value=login_result(9999999999)), \
                patch.object(perch_login, "RouteSession", FakeRoute), \
                patch.object(perch_export.SessionValidator, "validate", side_effect=RuntimeError(sensitive)):
            self.assertEqual(invoke(), (1, "", "worker: session_validation\n"))
            self.assertTrue(FakeRoute.instances[-1].closed)

        with patch.object(perch_login, "sign_in", return_value={"supabase_session": {}}):
            self.assertEqual(invoke(), (1, "", "worker: session_validation\n"))

        with patch.object(worker, "run", side_effect=RuntimeError(sensitive)):
            self.assertEqual(invoke(), (1, "", "worker: internal_error\n"))

    def test_missing_dependency_subprocess(self):
        # -S excludes site-packages so curl_cffi cannot be imported.
        result = subprocess.run([sys.executable, "-S", "-B", worker.__file__],
                                input=json.dumps(INPUT), text=True, capture_output=True, check=False)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, "")
        self.assertEqual(result.stderr, "worker: dependency_unavailable\n")

    def test_invalid_json_classification(self):
        result = subprocess.run([sys.executable, "-B", worker.__file__],
                                input="invalid secret-password", text=True, capture_output=True, check=False)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertEqual(result.stderr, "worker: input_validation\n")

    def test_sign_in_flow_with_mocked_network(self):
        class StartResponse:
            status_code = 302
            headers = {"location": "https://github.com/login/oauth/authorize?test=1"}

        class LoginClient:
            closed = False
            def get(self, *args, **kwargs):
                return StartResponse()
            def close(self):
                self.closed = True

        class LoginRoute:
            def __init__(self, proxy):
                self.connection = LoginClient()
                self.closed = False
                instances.append(self)
            def client(self):
                return self.connection
            def close(self):
                self.closed = True

        class Mailbox:
            def __init__(self, *args, **kwargs):
                pass
            def close(self):
                pass

        class Browser:
            def __init__(self, *args, session, **kwargs):
                self.session = session
            def close(self):
                self.session.close()

        instances = []
        from logicc_protocol import Pacer
        from open_design_github import GithubAccount
        account = GithubAccount(*(INPUT[k] for k in ("login", "password", "totp_secret", "mailbox_url")))
        with patch.object(perch_login, "RouteSession", LoginRoute), \
                patch.object(perch_login, "MailboxReader", Mailbox), \
                patch.object(perch_login, "GithubBrowser", Browser), \
                patch.object(perch_login, "authorize", return_value="https://chat.perchai.app/auth/callback?code=fake"), \
                patch.object(perch_login, "exchange", return_value=login_result(9999999999)["supabase_session"]):
            result = perch_login.sign_in(account, proxy=None, pacer=Pacer(0, 0), log=lambda _: None)
        self.assertEqual(result["user_id"], "user-1")
        self.assertTrue(instances[0].closed)
        self.assertTrue(instances[0].connection.closed)

    def test_direct_and_invalid_proxy(self):
        with patch.object(perch_login, "sign_in", return_value=login_result(9999999999)) as login, \
                patch.object(perch_login, "RouteSession", FakeRoute):
            worker.run({key: value for key, value in INPUT.items() if key != "proxy"})
            self.assertIsNone(login.call_args.kwargs["proxy"])
            with self.assertRaises(ValueError):
                worker.run({**INPUT, "proxy": "invalid"})


if __name__ == "__main__":
    unittest.main()
