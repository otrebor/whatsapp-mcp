import requests

import whatsapp
from tests.test_groups import DummyResponse


def _fake_get(monkeypatch, calls, response=None, error=None):
    def fake(url, headers=None, timeout=None):
        calls.append({"url": url, "headers": headers, "timeout": timeout})
        if error:
            raise error
        return response

    monkeypatch.setattr(whatsapp.requests, "get", fake)


def test_bridge_status_connected(monkeypatch):
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")
    _fake_get(monkeypatch, calls, DummyResponse(payload={"status": "ok", "connected": True}))

    result = whatsapp.bridge_status()

    assert result["reachable"] is True
    assert result["connected"] is True
    assert calls[0]["url"].endswith("/health")
    assert calls[0]["headers"] == {"Authorization": "Bearer test-token"}
    assert calls[0]["timeout"]


def test_bridge_status_disconnected(monkeypatch):
    payload = {"status": "disconnected", "connected": False}
    _fake_get(monkeypatch, [], DummyResponse(status_code=503, payload=payload))

    result = whatsapp.bridge_status()

    assert result["reachable"] is True
    assert result["connected"] is False
    assert "not connected" in result["message"]


def test_bridge_status_unreachable(monkeypatch):
    _fake_get(monkeypatch, [], error=requests.ConnectionError("refused"))

    result = whatsapp.bridge_status()

    assert result["reachable"] is False
    assert result["connected"] is False
    assert "not reachable" in result["message"]


def test_bridge_status_bad_token(monkeypatch):
    _fake_get(monkeypatch, [], DummyResponse(status_code=401, payload=None, text="Unauthorized"))

    result = whatsapp.bridge_status()

    assert result["reachable"] is True
    assert result["connected"] is False
    assert "401" in result["message"]


def test_bridge_status_awaiting_scan(monkeypatch):
    payload = {"status": "disconnected", "connected": False, "logged_in": False, "pairing_state": "awaiting_scan"}
    _fake_get(monkeypatch, [], DummyResponse(status_code=503, payload=payload))

    result = whatsapp.bridge_status()

    assert result["logged_in"] is False
    assert result["pairing_state"] == "awaiting_scan"
    assert "get_pairing_qr" in result["message"]


def test_bridge_status_linked_but_offline(monkeypatch):
    payload = {"status": "disconnected", "connected": False, "logged_in": True, "pairing_state": "paired"}
    _fake_get(monkeypatch, [], DummyResponse(status_code=503, payload=payload))

    result = whatsapp.bridge_status()

    assert result["logged_in"] is True
    assert "temporarily disconnected" in result["message"]


def test_bridge_status_logged_out_keeps_error(monkeypatch):
    payload = {
        "connected": False,
        "logged_in": False,
        "pairing_state": "logged_out",
        "pairing_error": "logged out: 401",
    }
    _fake_get(monkeypatch, [], DummyResponse(status_code=503, payload=payload))

    result = whatsapp.bridge_status()

    assert "restart" in result["message"]
    assert result["pairing_error"] == "logged out: 401"


def test_bridge_status_old_bridge_without_pairing_fields(monkeypatch):
    _fake_get(monkeypatch, [], DummyResponse(status_code=503, payload={"connected": False}))

    result = whatsapp.bridge_status()

    assert result["logged_in"] is None
    assert result["pairing_state"] is None
