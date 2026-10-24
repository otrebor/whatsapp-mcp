"""Re-linking without a bridge restart: get_pairing_qr after logout/timeout, and logout."""

import requests

import main
import whatsapp
from tests.test_groups import DummyResponse
from tests.test_pairing_qr import PNG, PngResponse

BASE = whatsapp.WHATSAPP_API_BASE_URL


def _fake_bridge(monkeypatch, gets, posts=None):
    """Answer requests by URL suffix. A list value is consumed one reply per
    call (the last one repeats); an exception value is raised."""
    calls = []

    def reply(method, url, routes):
        calls.append((method, url))
        for suffix, response in (routes or {}).items():
            if url.endswith(suffix):
                if isinstance(response, list):
                    response = response.pop(0) if len(response) > 1 else response[0]
                if isinstance(response, Exception):
                    raise response
                return response
        raise AssertionError(f"unexpected {method} {url}")

    monkeypatch.setattr(whatsapp.requests, "get", lambda url, headers=None, timeout=None: reply("GET", url, gets))
    monkeypatch.setattr(whatsapp.requests, "post", lambda url, headers=None, timeout=None: reply("POST", url, posts))
    monkeypatch.setattr(whatsapp, "PAIRING_QR_WAIT_S", 0.05)
    monkeypatch.setattr(whatsapp, "PAIRING_QR_POLL_S", 0)
    return calls


def _health(state, status_code=503):
    return DummyResponse(
        status_code=status_code, payload={"connected": False, "logged_in": False, "pairing_state": state}
    )


def test_get_pairing_qr_after_logout_waits_for_new_code(monkeypatch):
    calls = _fake_bridge(
        monkeypatch,
        {
            "/pairing/qr.png": [PngResponse(status_code=404, content=b""), PngResponse(headers={"X-Pairing-Seq": "1"})],
            "/health": _health("logged_out"),
        },
    )

    result = whatsapp.get_pairing_qr()

    assert result["success"] is True
    assert result["png"] == PNG
    assert ("POST", f"{BASE}/pairing/start") not in calls


def test_get_pairing_qr_logged_out_without_new_code_reports_status(monkeypatch):
    _fake_bridge(
        monkeypatch,
        {"/pairing/qr.png": PngResponse(status_code=404, content=b""), "/health": _health("logged_out")},
    )

    result = whatsapp.get_pairing_qr()

    assert result["success"] is False
    assert result["pairing_state"] == "logged_out"
    assert "get_pairing_qr" in result["message"]


def test_get_pairing_qr_timed_out_restarts_pairing(monkeypatch):
    calls = _fake_bridge(
        monkeypatch,
        {
            "/pairing/qr.png": [PngResponse(status_code=404, content=b""), PngResponse(headers={"X-Pairing-Seq": "4"})],
            "/health": _health("timed_out"),
        },
        {"/pairing/start": DummyResponse(status_code=202, payload={"success": True})},
    )

    result = whatsapp.get_pairing_qr()

    assert result["success"] is True
    assert result["seq"] == "4"
    assert ("POST", f"{BASE}/pairing/start") in calls


def test_get_pairing_qr_timed_out_on_old_bridge(monkeypatch):
    _fake_bridge(
        monkeypatch,
        {"/pairing/qr.png": PngResponse(status_code=404, content=b""), "/health": _health("timed_out")},
        {"/pairing/start": DummyResponse(status_code=404, payload=None, text="404 page not found")},
    )

    result = whatsapp.get_pairing_qr()

    assert result["success"] is False
    assert "restart the bridge" in result["message"]


def test_get_pairing_qr_timed_out_restart_race_still_waits(monkeypatch):
    _fake_bridge(
        monkeypatch,
        {
            "/pairing/qr.png": [PngResponse(status_code=404, content=b""), PngResponse()],
            "/health": _health("timed_out"),
        },
        {"/pairing/start": DummyResponse(status_code=409, payload={"success": False})},
    )

    assert whatsapp.get_pairing_qr()["success"] is True


def test_bridge_status_messages_point_to_get_pairing_qr(monkeypatch):
    for state in ("logged_out", "timed_out"):
        _fake_bridge(monkeypatch, {"/health": _health(state)})
        assert "get_pairing_qr" in whatsapp.bridge_status()["message"]


def test_logout_success(monkeypatch):
    calls = _fake_bridge(
        monkeypatch,
        {},
        {"/logout": DummyResponse(payload={"success": True, "message": "logged out; a new QR code..."})},
    )

    result = whatsapp.logout()

    assert result["success"] is True
    assert calls == [("POST", f"{BASE}/logout")]


def test_logout_maps_bridge_errors(monkeypatch):
    cases = [
        (DummyResponse(status_code=409, payload={"success": False, "message": "not logged in"}), "not logged in"),
        (
            DummyResponse(status_code=503, payload={"success": False, "message": "not connected to WhatsApp"}),
            "not connected",
        ),
        (DummyResponse(status_code=404, payload=None, text="404 page not found"), "Linked Devices"),
        (DummyResponse(status_code=401, payload=None), "401"),
        (DummyResponse(status_code=502, payload=None, text="bad gateway"), "502"),
    ]
    for response, expected in cases:
        _fake_bridge(monkeypatch, {}, {"/logout": response})
        result = whatsapp.logout()
        assert result["success"] is False, response.status_code
        assert expected in result["message"], (response.status_code, result)


def test_logout_bridge_unreachable(monkeypatch):
    error = requests.ConnectionError("refused")
    _fake_bridge(monkeypatch, {"/health": error}, {"/logout": error})

    result = whatsapp.logout()

    assert result["success"] is False
    assert result["reachable"] is False


def test_logout_tool_passes_through(monkeypatch):
    monkeypatch.setattr(main, "whatsapp_logout", lambda: {"success": True, "message": "ok"})

    assert main.logout() == {"success": True, "message": "ok"}
