"""Open the pairing QR code in the OS image viewer, for clients that can't render images.

The PNG goes into a private temp dir created here, and the viewer is launched
with a fixed command on that path only, never on caller input. The previous
file is removed on every call, so at most one (soon expired) code is on disk.
"""

import os
import shutil
import subprocess  # nosec B404 - fixed viewer commands on a path we create
import sys
import tempfile

_viewer_dir: str | None = None


def cleanup() -> None:
    """Delete the last QR image written by show(), if any."""
    global _viewer_dir
    if _viewer_dir:
        shutil.rmtree(_viewer_dir, ignore_errors=True)
        _viewer_dir = None


def show(png: bytes) -> str:
    """Write png to a fresh private temp dir, open it, and return its path."""
    global _viewer_dir
    cleanup()
    _viewer_dir = tempfile.mkdtemp(prefix="whatsapp-pairing-")  # mode 0700
    path = os.path.join(_viewer_dir, "pairing-qr.png")
    with open(path, "wb") as f:
        f.write(png)
    _open(path)
    return path


def _open(path: str) -> None:
    if sys.platform == "win32":
        os.startfile(path)  # type: ignore[attr-defined]  # nosec B606
    elif sys.platform == "darwin":
        subprocess.Popen(["open", path], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)  # nosec B603 B607
    else:
        subprocess.Popen(["xdg-open", path], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)  # nosec B603 B607
