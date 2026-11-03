package web

// This file serves the stylesheets of the pages. They are embedded in the
// binary, so a page loads nothing from another site: the
// Content-Security-Policy allows only this origin, and no inline style.
//
// static/bulma.min.css is Bulma 1.0.4 (https://bulma.io), copied unchanged
// from css/bulma.min.css of the npm package bulma@1.0.4. Its MIT license is in
// static/LICENSE-bulma.txt, which is embedded but not served: the license must
// go with every copy of the file, and the binary is one.
// TestBulmaIsTheReleasedFile checks its SHA-256.
// static/app.css has the few rules of this project that Bulma has no class
// for.

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
)

//go:embed static/*.css static/LICENSE-bulma.txt
var staticFS embed.FS

// staticPrefix starts the address of every static file.
const staticPrefix = "/static/"

// staticCacheControl lets a browser keep a static file for a year. The address
// of a file has the hash of its content, so a changed file gets a new address.
// It is the only answer that a browser may keep: a static file is the same for
// every user and holds nothing private.
const staticCacheControl = "public, max-age=31536000, immutable"

// codeNoStatic is the log code of an address under /static/ that is not a file.
const codeNoStatic = "no_static"

// staticFiles are the stylesheets, each at the address
// "/static/<first 16 hex digits of its SHA-256>/<name>".
type staticFiles struct {
	paths  map[string]string // name → address
	bodies map[string][]byte // address → content
}

// loadStatic reads the embedded stylesheets and gives each its address.
func loadStatic() (staticFiles, error) {
	files, err := fs.Glob(staticFS, "static/*.css")
	if err != nil {
		return staticFiles{}, fmt.Errorf("list static files: %w", err)
	}
	sf := staticFiles{paths: make(map[string]string), bodies: make(map[string][]byte)}
	for _, file := range files {
		body, err := staticFS.ReadFile(file)
		if err != nil {
			return staticFiles{}, fmt.Errorf("read %s: %w", file, err)
		}
		sum := sha256.Sum256(body)
		name := path.Base(file)
		addr := staticPrefix + hex.EncodeToString(sum[:8]) + "/" + name
		sf.paths[name] = addr
		sf.bodies[addr] = body
	}
	return sf, nil
}

// path returns the address of the static file with this name. The templates
// call it as {{static "bulma.min.css"}}.
func (sf staticFiles) path(name string) (string, error) {
	addr, ok := sf.paths[name]
	if !ok {
		return "", fmt.Errorf("web: no static file %q", name)
	}
	return addr, nil
}

// handleStatic answers a request under /static/. It needs no session (see
// staticCacheControl). It answers only GET and HEAD, and only the exact
// address of a file: any other address is not found.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if !safeMethod(r.Method) {
		w.Header().Set("Allow", "GET, HEAD")
		s.refuse(w, http.StatusMethodNotAllowed, codeBadMethod)
		return
	}
	body, ok := s.static.bodies[r.URL.Path]
	if !ok {
		s.refuse(w, http.StatusNotFound, codeNoStatic)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/css; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", staticCacheControl)
	_, _ = w.Write(body)
}
