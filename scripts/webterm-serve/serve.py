#!/usr/bin/env python3
"""Serve the V2V web client (webterm) with no server build.

The WASM web client is normally served by the V2V server at /web/. This
script reproduces the same static behavior from a directory, so the
browser sandbox is available without deploying the chat server: mount at
/web/, redirect / and /web to /web/, application/wasm for .wasm, and the
pre-compressed .br/.gz variants when the client accepts them.

It only reads files and returns their bytes unchanged; it never edits or
generates HTML/JS. Open http://127.0.0.1:8080/web/ and enter the chat
server URL in the form.
"""

import argparse
import http.server
import mimetypes
import os
import posixpath
import sys
import urllib.parse

WASM_MIME = "application/wasm"

# Best-first, matching the server: brotli then gzip.
ENCODINGS = (("br", "br"), ("gz", "gzip"))


class Handler(http.server.BaseHTTPRequestHandler):
    server_version = "V2VWebterm"
    protocol_version = "HTTP/1.1"

    def __init__(self, *args, directory="", **kwargs):
        self._root = directory
        super().__init__(*args, **kwargs)

    def log_message(self, format, *args):
        sys.stderr.write(f"{self.address_string()} - {format % args}\n")

    def do_HEAD(self):
        self._serve(head_only=True)

    def do_GET(self):
        self._serve(head_only=False)

    def _serve(self, head_only):
        raw = urllib.parse.urlsplit(self.path).path
        path = urllib.parse.unquote(raw)

        # Redirect the bare root and the prefix without a trailing slash.
        # "/web/" itself is served (index.html), never redirected, so the
        # check is on the exact strings rather than the normalized path.
        if path in ("", "/"):
            self._redirect("/web/")
            return
        if path == "/web":
            self._redirect("/web/")
            return

        # Resolve the whole path: a normalized path that no longer starts
        # with /web/ (e.g. "/web/../x") is refused, so no request can
        # reach outside the served directory. normpath drops a trailing
        # slash, so restore it for the directory case.
        had_slash = path.endswith("/")
        path = posixpath.normpath(path)
        if not path.startswith("/web/") and path != "/web":
            self.send_error(404)
            return
        if path == "/web":
            path = "/web/"
        elif had_slash and not path.endswith("/"):
            path += "/"

        name = path[len("/web/"):]
        if not name or name.endswith("/"):
            name = posixpath.join(name, "index.html")

        full: str = os.path.join(self._root, name)
        if os.path.isdir(full):
            name = posixpath.join(name, "index.html")
            full = os.path.join(self._root, name)
        if not os.path.isfile(full):
            self.send_error(404)
            return

        # Pre-compressed sibling, unless the request already names one.
        if not name.endswith((".br", ".gz")):
            accept = self.headers.get("Accept-Encoding", "")
            for suffix, encoding in ENCODINGS:
                if not _accepts(accept, encoding):
                    continue
                variant = full + "." + suffix
                if os.path.isfile(variant):
                    self._send_file(variant, name, encoding, head_only)
                    return

        self._send_file(full, name, None, head_only)

    def _redirect(self, location):
        self.send_response(301)
        self.send_header("Location", location)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _send_file(self, full, name, encoding, head_only):
        try:
            with open(full, "rb") as fh:
                body = fh.read()
        except OSError:
            self.send_error(404)
            return

        self.send_response(200)
        self.send_header("Content-Type", _content_type(name))
        self.send_header("Vary", "Accept-Encoding")
        if encoding:
            self.send_header("Content-Encoding", encoding)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if not head_only:
            self.wfile.write(body)


def _content_type(name):
    ext = posixpath.splitext(name)[1].lower()
    if ext == ".wasm":
        return WASM_MIME
    guess, _ = mimetypes.guess_type(name)
    return guess or "application/octet-stream"


def _accepts(header, enc):
    """Mirror the server: explicit token, honoring q=0, no wildcard."""
    for part in header.split(","):
        part = part.strip()
        if ";" in part:
            token, _, params = part.partition(";")
            q = params.strip()
            if q.startswith("q="):
                try:
                    if float(q[2:]) <= 0:
                        continue
                except ValueError:
                    pass
            part = token.strip()
        if part.lower() == enc.lower():
            return True
    return False


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    parser = argparse.ArgumentParser(
        description="Serve the V2V web client (webterm) over HTTP.",
    )
    parser.add_argument("--port", type=int, default=8080)
    parser.add_argument("--bind", default="127.0.0.1")
    parser.add_argument(
        "--dir",
        default=here,
        help="webterm directory to serve (default: this script's directory)",
    )
    args = parser.parse_args()

    root = os.path.abspath(args.dir)
    if not os.path.isdir(root):
        parser.error(f"directory not found: {root}")
    if not os.path.isfile(os.path.join(root, "index.html")):
        parser.error(f"no index.html in {root} (build with 'make web')")

    def factory(*a, **kw):
        return Handler(*a, directory=root, **kw)

    httpd = http.server.ThreadingHTTPServer((args.bind, args.port), factory)
    url = f"http://{args.bind}:{args.port}/web/"
    print(f"Serving {root} at {url}")
    print("Open the URL and enter your chat server address in the form.")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\nStopped.")


if __name__ == "__main__":
    main()
