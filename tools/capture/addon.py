"""wb2api capture addon for mitmproxy.

Writes one readable JSON file per copilot.tencent.com flow into
$WB2API_CAPTURE_DIR (default: ./captures), so captures can be analyzed
without the mitmproxy toolchain.

Run via:
    mitmdump -s addon.py --listen-port 8080 --set confdir=~/.wb2api-mitm
"""
import base64
import json
import os
import time

import mitmproxy.http

OUT = os.environ.get("WB2API_CAPTURE_DIR", "captures")
INTERESTING_HOSTS = ("copilot.tencent.com", "ssl.tencent.com", "codebuddy.cn")

os.makedirs(OUT, exist_ok=True)
_seq = [0]


def _snapshot(flow: mitmproxy.http.HTTPFlow) -> dict:
    req = flow.request
    d = {
        "seq": _seq[0],
        "ts": time.strftime("%Y-%m-%d %H:%M:%S"),
        "method": req.method,
        "url": req.pretty_url,
        "request_headers": dict(req.headers),
        "request_body": None,
        "response_status": None,
        "response_headers": None,
        "response_body": None,
    }
    if req.raw_content is not None:
        d["request_body"] = _encode(req.raw_content)
    if flow.response is not None:
        resp = flow.response
        d["response_status"] = resp.status_code
        d["response_headers"] = dict(resp.headers)
        if resp.raw_content is not None:
            d["response_body"] = _encode(resp.raw_content)
    return d


def _encode(b: bytes):
    """Return a JSON-friendly value for raw bytes.

    Text is stored inline; binary is stored as base64 with a marker so it can
    be decoded losslessly during analysis.
    """
    try:
        return b.decode("utf-8")
    except UnicodeDecodeError:
        return {"__base64__": base64.b64encode(b).decode("ascii")}


def _write(flow: mitmproxy.http.HTTPFlow) -> None:
    if not any(h in flow.request.pretty_host for h in INTERESTING_HOSTS):
        return
    _seq[0] += 1
    name = f"{_seq[0]:04d}-{flow.request.method.lower()}.json"
    with open(os.path.join(OUT, name), "w", encoding="utf-8") as f:
        json.dump(_snapshot(flow), f, ensure_ascii=False, indent=2)


def request(flow: mitmproxy.http.HTTPFlow) -> None:
    pass


def response(flow: mitmproxy.http.HTTPFlow) -> None:
    _write(flow)


def error(flow: mitmproxy.http.HTTPFlow) -> None:
    _write(flow)
