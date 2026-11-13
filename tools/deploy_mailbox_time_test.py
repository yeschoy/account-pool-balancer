"""
取验证邮件的时序判断测试

背景（2026-11-13 线上事故）：
  deploy.com 的验证信经 Mandrill 投递，实测从注册到到达可以超过 1 小时
  （09:37:31Z 注册 → 10:43:44Z 到信，差 66 分钟），而注册机只等 240s。
  更关键的是：**信其实已经到了收件箱**，旧的 id 增量判断却会把它当成
  "旧邮件"跳过——因为 before_ids 是在注册前抓的，只要 mida 在轮询开始前
  就把信放进结果集，这封信就永远命中 after_ids。

  所以这里固定住两条规则：
    1. 有 receivedAt 时以**到达时间**为准（since_ts 之前的不算）；
    2. 没有 receivedAt 才退回 id 增量。
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import deploy_registrar as R  # noqa: E402


DEPLOY_BODY = (
    'Welcome to Deploy. <https://deploy.com/verify-email?token=b98w61WsddxTNEfA2Fve&'
    'redirect_uri=https%3A%2F%2Fdeploy.com%2Fexplore>'
)


def _mail(mid, received_at):
    return {
        "id": mid,
        "receivedAt": received_at,
        "fromAddress": "do-not-reply_at_deploy_com_z7psf6c8y2cv87_43766893@icloud.com",
        "textContent": DEPLOY_BODY,
        "htmlContent": "",
    }


def _reader(mails):
    """造一个 MailboxReader，fetch 返回给定邮件。"""
    mb = R.Mailbox(email="a@icloud.com", url="https://icloud.mida.vip/messages/a%40icloud.com?token=t",
                   host="icloud.mida.vip", token="t", alias="a%40icloud.com")
    r = R.MailboxReader(mb)
    r.fetch = lambda range_="all": list(mails)  # type: ignore[assignment]
    return r


def test_late_mail_is_accepted_by_time_not_id():
    """66 分钟才到的信：只要晚于 since_ts 就必须被认到。

    这正是线上那条候选的状态——信在箱子里，但 id 早已进了 before_ids。
    """
    late = _mail(270176, "2026-11-13T10:43:44.000Z")
    since = R.datetime.fromisoformat("2026-11-13T09:37:31+00:00").timestamp()
    got = _reader([late]).wait_for_link({270176}, timeout=1, poll=0.01, since_ts=since)
    assert got is not None, "晚到的验证信必须被认到，不能因为 id 已在 before_ids 里就跳过"
    assert got[0] == "b98w61WsddxTNEfA2Fve"
    assert got[1]["id"] == 270176


def test_mail_before_since_ts_is_ignored():
    """比注册时刻更早的残留验证信不能算这次注册的。"""
    old = _mail(111, "2026-11-13T08:00:00.000Z")
    since = R.datetime.fromisoformat("2026-11-13T09:37:31+00:00").timestamp()
    assert _reader([old]).wait_for_link(set(), timeout=0.3, poll=0.05, since_ts=since) is None


def test_non_deploy_mail_never_matches():
    """Token Harbor 的旧信不能被当成 deploy 验证信。"""
    other = {
        "id": 171062,
        "receivedAt": "2026-11-13T10:43:44.000Z",
        "fromAddress": "verify_at_tokenharbor_ai_x@icloud.com",
        "textContent": "https://tokenharbor.ai/verify-email?token=abc",
        "htmlContent": "",
    }
    assert _reader([other]).wait_for_link(set(), timeout=0.3, poll=0.05) is None


def test_without_received_at_falls_back_to_id_delta():
    """没有 receivedAt 的邮件退回 id 增量，保持旧行为。"""
    no_ts = {"id": 5, "fromAddress": "deploy@deploy.com", "textContent": DEPLOY_BODY, "htmlContent": ""}
    assert _reader([no_ts]).wait_for_link({5}, timeout=0.3, poll=0.05) is None
    got = _reader([no_ts]).wait_for_link(set(), timeout=0.3, poll=0.05)
    assert got is not None and got[1]["id"] == 5


def test_received_ts_parses_mida_format():
    """mida 返回的是 ISO-8601 + Z，按 UTC 解读（与截图 18:43:44 一致）。"""
    ts = R.MailboxReader._received_ts({"receivedAt": "2026-11-13T10:43:44.000Z"})
    assert ts is not None
    assert abs(ts - R.datetime.fromisoformat("2026-11-13T10:43:44+00:00").timestamp()) < 0.001
    # Go 风格的纳秒精度也要能解析
    assert R.MailboxReader._received_ts({"receivedAt": "2026-11-13T10:43:44.062511511Z"}) is not None
    assert R.MailboxReader._received_ts({}) is None


def test_picks_newest_verification_mail():
    """重新认证接码扫全箱时，必须用**最新**的验证信，不能用旧的。

    旧 token 可能对应上一次注册已经失效；只有最新那封才代表当前状态。
    """
    older = _mail(100, "2026-11-13T09:00:00.000Z")
    older["textContent"] = DEPLOY_BODY.replace("b98w61WsddxTNEfA2Fve", "OLDtokenOLDtoken")
    newer = _mail(200, "2026-11-13T10:43:44.000Z")
    got = _reader([older, newer]).wait_for_link(set(), timeout=1, poll=0.01, since_ts=0.0)
    assert got is not None
    assert got[1]["id"] == 200, "应当选到达时间最新的那封"
    assert got[0] == "b98w61WsddxTNEfA2Fve"


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn()
            print("ok", name)
