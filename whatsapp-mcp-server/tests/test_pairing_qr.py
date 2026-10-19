"""get_pairing_qr: bridge client and MCP tool."""

import base64
import os

import requests

import main
import pairing_viewer
import whatsapp
from tests.test_groups import DummyResponse

PNG = b"\x89PNG\r\n\x1a\nfake"


class PngResponse(DummyResponse):
    def __init__(self, status_code=200, content=PNG, headers=None, payload=None):
        super().__init__(status_code=status_code, payload=payload)
        self.content = content
        self.headers = headers or {}


def _route(monkeypatch, routes, calls):
    """Answer GETs by URL suffix; a route value that is an exception is raised."""

    def fake(url, headers=None, timeout=None):
        calls.append(url)
        for suffix, response in routes.items():
            if url.endswith(suffix):
                if isinstance(response, Exception):
                    raise response
                return response
        raise AssertionError(f"unexpected GET {url}")

    monkeypatch.setattr(whatsapp.requests, "get", fake)


def test_get_pairing_qr_returns_png(monkeypatch):
    calls = []
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", "test-token")
    _route(monkeypatch, {"/pairing/qr.png": PngResponse(headers={"X-Pairing-Seq": "3"})}, calls)

    result = whatsapp.get_pairing_qr()

    assert result["success"] is True
    assert result["png"] == PNG
    assert result["seq"] == "3"
    assert calls == [f"{whatsapp.WHATSAPP_API_BASE_URL}/pairing/qr.png"]


def test_get_pairing_qr_already_linked(monkeypatch):
    health = {"connected": True, "logged_in": True, "pairing_state": "paired"}
    _route(
        monkeypatch,
        {"/pairing/qr.png": PngResponse(status_code=404, content=b""), "/health": DummyResponse(payload=health)},
        [],
    )

    result = whatsapp.get_pairing_qr()

    assert result["success"] is True
    assert result["message"] == "already linked"
    assert "png" not in result


def test_get_pairing_qr_unreachable(monkeypatch):
    error = requests.ConnectionError("refused")
    _route(monkeypatch, {"/pairing/qr.png": error, "/health": error}, [])

    result = whatsapp.get_pairing_qr()

    assert result["success"] is False
    assert result["reachable"] is False
    assert "not reachable" in result["message"]


def test_get_pairing_qr_logged_out(monkeypatch):
    health = {"connected": False, "logged_in": False, "pairing_state": "logged_out"}
    _route(
        monkeypatch,
        {
            "/pairing/qr.png": PngResponse(status_code=404, content=b""),
            "/health": DummyResponse(status_code=503, payload=health),
        },
        [],
    )

    result = whatsapp.get_pairing_qr()

    assert result["success"] is False
    assert result["pairing_state"] == "logged_out"
    assert "restart" in result["message"]


def test_get_pairing_qr_old_bridge(monkeypatch):
    _route(
        monkeypatch,
        {
            "/pairing/qr.png": PngResponse(status_code=404, content=b""),
            "/health": DummyResponse(status_code=503, payload={"connected": False}),
        },
        [],
    )

    result = whatsapp.get_pairing_qr()

    assert result["success"] is False
    assert "terminal" in result["message"]


def test_get_pairing_qr_bad_token(monkeypatch):
    _route(monkeypatch, {"/pairing/qr.png": PngResponse(status_code=401, content=b"")}, [])

    result = whatsapp.get_pairing_qr()

    assert result["success"] is False
    assert "401" in result["message"]


def test_tool_returns_image_and_instructions(monkeypatch):
    monkeypatch.setattr(main, "whatsapp_get_pairing_qr", lambda: {"success": True, "png": PNG, "seq": "2"})

    image, text = main.get_pairing_qr()

    content = image.to_image_content()
    assert content.mimeType == "image/png"
    assert base64.b64decode(content.data) == PNG
    assert "#2" in text
    assert "Linked Devices" in text


def test_tool_passes_through_status_without_code(monkeypatch):
    status = {"success": True, "message": "already linked"}
    monkeypatch.setattr(main, "whatsapp_get_pairing_qr", lambda: status)

    assert main.get_pairing_qr() == status


def test_tool_open_viewer_writes_private_file_and_cleans_up(monkeypatch):
    opened = []
    monkeypatch.setattr(pairing_viewer, "_open", opened.append)
    monkeypatch.setattr(main, "whatsapp_get_pairing_qr", lambda: {"success": True, "png": PNG, "seq": "1"})

    _, text = main.get_pairing_qr(open_viewer=True)

    assert len(opened) == 1
    path = opened[0]
    with open(path, "rb") as f:
        assert f.read() == PNG
    assert path in text

    # The next call (here: already linked) removes the previous image.
    monkeypatch.setattr(main, "whatsapp_get_pairing_qr", lambda: {"success": True, "message": "already linked"})
    main.get_pairing_qr(open_viewer=True)
    assert not os.path.exists(path)
