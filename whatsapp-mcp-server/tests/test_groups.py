import whatsapp


class DummyResponse:
    def __init__(self, status_code=200, payload=None, text="OK"):
        self.status_code = status_code
        self._payload = payload
        self.text = text

    def json(self):
        if self._payload is None:
            raise ValueError("no JSON")
        return self._payload


def _capture(monkeypatch, method, calls, response):
    def fake(url, json=None, params=None, headers=None):
        calls.append({"url": url, "json": json, "params": params, "headers": headers})
        return response

    monkeypatch.setattr(whatsapp.requests, method, fake)


def test_get_group_participants_calls_bridge(monkeypatch):
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")
    payload = {"success": True, "jid": "123@g.us", "participants": [{"jid": "1@s.whatsapp.net"}]}
    _capture(monkeypatch, "get", calls, DummyResponse(payload=payload))

    result = whatsapp.get_group_participants("123")

    assert result == payload
    assert calls[0]["url"].endswith("/group-info")
    assert calls[0]["params"] == {"jid": "123@g.us"}
    assert calls[0]["headers"] == {"Authorization": "Bearer test-token"}


def test_get_group_participants_rejects_non_group_jid(monkeypatch):
    calls = []
    _capture(monkeypatch, "get", calls, DummyResponse(payload={}))

    result = whatsapp.get_group_participants("12025551234@s.whatsapp.net")

    assert result["success"] is False
    assert calls == []


def test_get_group_invite_link_calls_bridge(monkeypatch):
    calls = []
    payload = {"success": True, "jid": "123@g.us", "link": "https://chat.whatsapp.com/abc"}
    _capture(monkeypatch, "get", calls, DummyResponse(payload=payload))

    result = whatsapp.get_group_invite_link(" 123 ")

    assert result == payload
    assert calls[0]["url"].endswith("/group-invite-link")
    assert calls[0]["params"] == {"jid": "123@g.us"}


def test_get_group_invite_link_rejects_non_group_jid(monkeypatch):
    calls = []
    _capture(monkeypatch, "get", calls, DummyResponse(payload={}))

    assert whatsapp.get_group_invite_link("12025551234@s.whatsapp.net")["success"] is False
    assert whatsapp.get_group_invite_link("")["success"] is False
    assert calls == []


def test_group_non_json_error(monkeypatch):
    calls = []
    _capture(monkeypatch, "get", calls, DummyResponse(status_code=401, text="Unauthorized"))

    result = whatsapp.get_group_participants("123@g.us")

    assert result == {"success": False, "message": "Error: HTTP 401 - Unauthorized"}
