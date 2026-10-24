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


def test_add_group_participants_calls_bridge(monkeypatch):
    calls = []
    payload = {"success": True, "message": "Added 2 of 2 participants", "added": 2, "failed": []}
    _capture(monkeypatch, "post", calls, DummyResponse(payload=payload))

    result = whatsapp.add_group_participants("123", ["+41791234567", "12025551234"])

    assert result == payload
    assert calls[0]["url"].endswith("/group-participants-add")
    assert calls[0]["json"] == {"jid": "123@g.us", "participants": ["41791234567", "12025551234"]}


def test_add_group_participants_validates_before_calling_bridge(monkeypatch):
    calls = []
    _capture(monkeypatch, "post", calls, DummyResponse(payload={}))

    assert whatsapp.add_group_participants("123", [])["success"] is False
    assert whatsapp.add_group_participants("123", [" "])["success"] is False
    assert whatsapp.add_group_participants("1@s.whatsapp.net", ["41791234567"])["success"] is False
    assert calls == []


def test_remove_group_participants_calls_bridge(monkeypatch):
    calls = []
    payload = {"success": True, "message": "Removed 2 of 2 participants", "removed": 2, "failed": []}
    _capture(monkeypatch, "post", calls, DummyResponse(payload=payload))

    result = whatsapp.remove_group_participants("123", ["+41791234567", "900@lid"])

    assert result == payload
    assert calls[0]["url"].endswith("/group-participants-remove")
    assert calls[0]["json"] == {"jid": "123@g.us", "participants": ["41791234567", "900@lid"]}


def test_remove_group_participants_validates_before_calling_bridge(monkeypatch):
    calls = []
    _capture(monkeypatch, "post", calls, DummyResponse(payload={}))

    assert whatsapp.remove_group_participants("123", [])["success"] is False
    assert whatsapp.remove_group_participants("123", [" "])["success"] is False
    assert whatsapp.remove_group_participants("1@s.whatsapp.net", ["41791234567"])["success"] is False
    assert calls == []


def test_leave_group_calls_bridge(monkeypatch):
    calls = []
    _capture(monkeypatch, "post", calls, DummyResponse(payload={"success": True, "message": "Left group"}))

    result = whatsapp.leave_group(" 123 ")

    assert result["success"] is True
    assert calls[0]["url"].endswith("/group-leave")
    assert calls[0]["json"] == {"jid": "123@g.us"}


def test_leave_group_rejects_non_group_jid(monkeypatch):
    calls = []
    _capture(monkeypatch, "post", calls, DummyResponse(payload={}))

    assert whatsapp.leave_group("")["success"] is False
    assert whatsapp.leave_group("41791234567@s.whatsapp.net")["success"] is False
    assert calls == []


def test_delete_chat_calls_bridge(monkeypatch):
    calls = []
    _capture(monkeypatch, "post", calls, DummyResponse(payload={"success": True, "message": "deleted"}))

    result = whatsapp.delete_chat(" +41791234567 ")

    assert result["success"] is True
    assert calls[0]["url"].endswith("/chat-delete")
    assert calls[0]["json"] == {"chat_jid": "41791234567"}


def test_delete_chat_requires_jid(monkeypatch):
    calls = []
    _capture(monkeypatch, "post", calls, DummyResponse(payload={}))

    assert whatsapp.delete_chat(" ")["success"] is False
    assert calls == []


def test_set_group_description_calls_bridge(monkeypatch):
    calls = []
    _capture(monkeypatch, "post", calls, DummyResponse(payload={"success": True, "message": "ok"}))

    result = whatsapp.set_group_description("123", "Hello *all*")

    assert result["success"] is True
    assert calls[0]["url"].endswith("/group-topic")
    assert calls[0]["json"] == {"jid": "123@g.us", "topic": "Hello *all*"}


def test_set_group_description_rejects_non_group_jid(monkeypatch):
    calls = []
    _capture(monkeypatch, "post", calls, DummyResponse(payload={}))

    assert whatsapp.set_group_description("12025551234@s.whatsapp.net", "x")["success"] is False
    assert calls == []


def test_create_group_sends_normalized_payload(monkeypatch):
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")
    _capture(monkeypatch, "post", calls, DummyResponse(payload={"success": True, "jid": "9@g.us"}))

    result = whatsapp.create_group("  Party  ", ["+41791234567", " 12025551234@s.whatsapp.net "])

    assert result["success"] is True
    assert calls[0]["url"].endswith("/group-create")
    assert calls[0]["json"] == {
        "name": "Party",
        "participants": ["41791234567", "12025551234@s.whatsapp.net"],
    }


def test_create_group_validates_before_calling_bridge(monkeypatch):
    calls = []
    _capture(monkeypatch, "post", calls, DummyResponse(payload={}))

    assert whatsapp.create_group(" ", ["41791234567"])["success"] is False
    assert whatsapp.create_group("Party", [])["success"] is False
    assert whatsapp.create_group("Party", ["41791234567", " "])["success"] is False
    assert calls == []


def test_group_error_body_is_surfaced(monkeypatch):
    calls = []
    error = {"success": False, "message": "Failed to create group: 406"}
    _capture(monkeypatch, "post", calls, DummyResponse(status_code=502, payload=error))

    assert whatsapp.create_group("Party", ["41791234567"]) == error


def test_group_non_json_error(monkeypatch):
    calls = []
    _capture(monkeypatch, "get", calls, DummyResponse(status_code=401, text="Unauthorized"))

    result = whatsapp.get_group_participants("123@g.us")

    assert result == {"success": False, "message": "Error: HTTP 401 - Unauthorized"}
