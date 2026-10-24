"""Live pairing page: locating it on the bridge and opening it from get_pairing_qr."""

from urllib.parse import parse_qs, urlsplit

import requests

import main
import pairing_viewer
import whatsapp
from tests.test_pairing_qr import PNG, PngResponse

TOKEN = "test-token-with/odd+chars"


def _fake_page(monkeypatch, response, calls=None):
    def fake(url, headers=None, timeout=None):
        if calls is not None:
            calls.append({"url": url, "headers": headers})
        if isinstance(response, Exception):
            raise response
        return response

    monkeypatch.setattr(whatsapp.requests, "get", fake)
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", TOKEN)


def test_pairing_page_url_puts_token_only_in_fragment(monkeypatch):
    calls = []
    _fake_page(monkeypatch, PngResponse(content=b"<html>", headers={"Content-Type": "text/html; charset=utf-8"}), calls)

    open_url, display_url = whatsapp.pairing_page_url()

    parts = urlsplit(open_url)
    assert display_url == f"{whatsapp.WHATSAPP_API_BASE_URL.removesuffix('/api')}/pairing"
    assert open_url.startswith(display_url + "#")
    assert parse_qs(parts.fragment) == {"token": [TOKEN]}
    assert parts.query == ""
    assert TOKEN not in display_url
    # The probe needs no token: the page is public, its API calls are not.
    assert calls == [{"url": display_url, "headers": None}]


def test_pairing_page_url_none_for_old_bridge(monkeypatch):
    _fake_page(monkeypatch, PngResponse(status_code=404, content=b"404 page not found"))
    assert whatsapp.pairing_page_url() is None


def test_pairing_page_url_none_when_unreachable(monkeypatch):
    _fake_page(monkeypatch, requests.ConnectionError("refused"))
    assert whatsapp.pairing_page_url() is None


def test_pairing_page_url_none_without_token(monkeypatch):
    _fake_page(monkeypatch, PngResponse(headers={"Content-Type": "text/html"}))
    monkeypatch.delenv("WHATSAPP_BRIDGE_TOKEN")
    monkeypatch.setattr(whatsapp, "_BRIDGE_TOKEN_PATH", "/nonexistent/.bridge-token")
    assert whatsapp.pairing_page_url() is None


def test_pairing_page_url_none_for_custom_api_path(monkeypatch):
    monkeypatch.setattr(whatsapp, "WHATSAPP_API_BASE_URL", "http://localhost:8080/bridge")
    monkeypatch.setenv("WHATSAPP_BRIDGE_TOKEN", TOKEN)
    assert whatsapp.pairing_page_url() is None


def _page_available(monkeypatch, opened):
    monkeypatch.setattr(
        main,
        "whatsapp_pairing_page_url",
        lambda: ("http://127.0.0.1:8080/pairing#token=secret", "http://127.0.0.1:8080/pairing"),
    )
    monkeypatch.setattr(pairing_viewer, "open_page", opened.append)
    monkeypatch.setattr(pairing_viewer, "_open", lambda path: opened.append(("image-viewer", path)))


def test_tool_opens_live_page_instead_of_image_viewer(monkeypatch):
    opened = []
    _page_available(monkeypatch, opened)
    monkeypatch.setattr(main, "whatsapp_get_pairing_qr", lambda: {"success": True, "png": PNG, "seq": "2"})

    image, text = main.get_pairing_qr(open_viewer=True)

    assert opened == ["http://127.0.0.1:8080/pairing#token=secret"]
    assert "http://127.0.0.1:8080/pairing" in text
    assert "secret" not in text
    assert image.to_image_content().mimeType == "image/png"


def test_tool_opens_live_page_while_no_code_yet(monkeypatch):
    opened = []
    _page_available(monkeypatch, opened)
    status = {"success": False, "pairing_state": "logged_out", "message": "Bridge was logged out"}
    monkeypatch.setattr(main, "whatsapp_get_pairing_qr", lambda: status)

    result = main.get_pairing_qr(open_viewer=True)

    assert opened == ["http://127.0.0.1:8080/pairing#token=secret"]
    assert result["pairing_state"] == "logged_out"
    assert result["message"].startswith("Bridge was logged out ")
    assert "live pairing page" in result["message"]
    assert "secret" not in result["message"]


def test_tool_does_not_open_page_without_open_viewer(monkeypatch):
    def unexpected():
        raise AssertionError("page lookup without open_viewer")

    monkeypatch.setattr(main, "whatsapp_pairing_page_url", unexpected)
    monkeypatch.setattr(main, "whatsapp_get_pairing_qr", lambda: {"success": True, "png": PNG, "seq": "1"})

    _, text = main.get_pairing_qr()

    assert "live pairing page" not in text
