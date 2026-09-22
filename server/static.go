package main

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CleveTok3125/V2V/internal/env"
)

// webtermDir is the directory holding the web assets, resolved once at
// startup. It defaults to the cwd-relative "webterm" so tests and
// source-tree runs keep working.
var webtermDir = "webterm"

// executableDir returns the directory of the running binary, or "" when
// it cannot be determined.
func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// resolveWebtermDir picks the web asset directory in this order:
//  1. WEBTERM_DIR when set (explicit operator override),
//  2. "<exeDir>/webterm" when exeDir is non-empty and that dir exists,
//  3. "webterm" relative to the current working directory.
//
// This keeps a binary launched from an arbitrary cwd (e.g. a systemd
// unit or an extracted release tarball) able to find its assets next to
// itself, without breaking source-tree and Docker runs.
func resolveWebtermDir(exeDir string) string {
	if dir := strings.TrimSpace(env.WebtermDir()); dir != "" {
		return dir
	}
	if exeDir != "" {
		candidate := filepath.Join(exeDir, "webterm")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
	}
	return "webterm"
}

// webFilesHandler serves the static asset directory with transparent support
// for precompressed variants: when the client advertises br/gzip support and
// a matching "<name>.br" / "<name>.gz" exists next to the original (both are
// produced once by `make web`), the variant is streamed instead with the
// proper Content-Encoding. This shrinks big assets such as app.wasm to a
// fraction of their size without spending any CPU compressing per request.
func webFilesHandler(dir string) http.Handler {
	fsys := os.DirFS(dir)
	fileServer := http.FileServer(http.Dir(dir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")

		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
			if name != "" && !strings.HasSuffix(name, "/") &&
				!strings.HasSuffix(name, ".br") && !strings.HasSuffix(name, ".gz") {
				for _, v := range []struct{ suffix, encoding string }{
					{".br", "br"},
					{".gz", "gzip"},
				} {
					if !acceptsEncoding(r.Header.Get("Accept-Encoding"), v.encoding) {
						continue
					}
					f, err := fsys.Open(name + v.suffix)
					if err != nil {
						continue
					}
					st, err := f.Stat()
					if err != nil || st.IsDir() {
						f.Close()
						continue
					}
					rs, ok := f.(io.ReadSeeker)
					if !ok {
						f.Close()
						continue
					}
					defer f.Close()
					w.Header().Set("Content-Encoding", v.encoding)
					ext := path.Ext(strings.TrimSuffix(name, v.suffix))
					if ct := mime.TypeByExtension(ext); ct != "" {
						w.Header().Set("Content-Type", ct)
					}
					http.ServeContent(w, r, st.Name(), st.ModTime(), rs)
					return
				}
			}
		}

		// No directory listings: a directory without its own index
		// answers 404 instead of exposing the file list.
		if name := strings.TrimPrefix(path.Clean(r.URL.Path), "/"); name != "" {
			if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.IsDir() {
				if _, err := os.Stat(filepath.Join(dir, name, "index.html")); err != nil {
					http.NotFound(w, r)
					return
				}
			}
		}

		fileServer.ServeHTTP(w, r)
	})
}

// acceptsEncoding reports whether the Accept-Encoding header explicitly lists
// enc (the wildcard form is intentionally ignored: every real browser names
// br/gzip explicitly).
func acceptsEncoding(header, enc string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if i := strings.IndexByte(part, ';'); i >= 0 {
			if q := strings.TrimSpace(part[i+1:]); strings.HasPrefix(q, "q=") {
				if v, err := strconv.ParseFloat(q[2:], 64); err == nil && v <= 0 {
					continue
				}
			}
			part = strings.TrimSpace(part[:i])
		}
		if strings.EqualFold(part, enc) {
			return true
		}
	}
	return false
}
