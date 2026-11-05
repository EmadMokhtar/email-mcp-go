package web

// This file serves the static files of the pages: the stylesheet, the script
// and the icon. They are embedded in the binary, so a page loads nothing from
// another site: the Content-Security-Policy allows only this origin, and no
// inline script or style.
//
// static/app.css has every style of the pages. static/app.js only makes a form
// answer at once while its request runs. Every form works without it.

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

//go:embed static/*.css static/*.js static/*.svg
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

// staticTypes is the Content-Type of each kind of static file. The list is
// fixed here, not taken from the system, so every computer serves the same
// type: the browser refuses a stylesheet of another type (nosniff).
var staticTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
	".svg": "image/svg+xml",
}

// staticFile is one static file: its content and its Content-Type.
type staticFile struct {
	body        []byte
	contentType string
}

// staticFiles are the static files, each at the address
// "/static/<first 16 hex digits of its SHA-256>/<name>".
type staticFiles struct {
	paths map[string]string     // name → address
	files map[string]staticFile // address → file
}

// loadStatic reads the embedded files and gives each its address.
func loadStatic() (staticFiles, error) {
	names, err := fs.Glob(staticFS, "static/*")
	if err != nil {
		return staticFiles{}, fmt.Errorf("list static files: %w", err)
	}
	sf := staticFiles{paths: make(map[string]string), files: make(map[string]staticFile)}
	for _, file := range names {
		name := path.Base(file)
		contentType, ok := staticTypes[path.Ext(name)]
		if !ok {
			return staticFiles{}, fmt.Errorf("static file %s: no Content-Type for its extension", file)
		}
		body, err := staticFS.ReadFile(file)
		if err != nil {
			return staticFiles{}, fmt.Errorf("read %s: %w", file, err)
		}
		sum := sha256.Sum256(body)
		addr := staticPrefix + hex.EncodeToString(sum[:8]) + "/" + name
		sf.paths[name] = addr
		sf.files[addr] = staticFile{body: body, contentType: contentType}
	}
	return sf, nil
}

// path returns the address of the static file with this name. The templates
// call it as {{static "app.css"}}.
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
	file, ok := s.static.files[r.URL.Path]
	if !ok {
		s.refuse(w, http.StatusNotFound, codeNoStatic)
		return
	}
	h := w.Header()
	h.Set("Content-Type", file.contentType)
	h.Set("Content-Length", strconv.Itoa(len(file.body)))
	h.Set("Cache-Control", staticCacheControl)
	_, _ = w.Write(file.body)
}
