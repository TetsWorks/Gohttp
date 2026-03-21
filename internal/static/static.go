package static

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TetsWorks/Gohttp/internal/parser"
	"github.com/TetsWorks/Gohttp/internal/router"
)

type Config struct {
	Root        string
	Index       string
	Browse      bool
	CacheMaxAge int
	Prefix      string
}

func Handler(cfg Config) router.HandlerFunc {
	if cfg.Index == "" { cfg.Index = "index.html" }
	if cfg.CacheMaxAge == 0 { cfg.CacheMaxAge = 3600 }
	return func(w parser.ResponseWriter, r *parser.Request) {
		urlPath := r.Path
		if cfg.Prefix != "" { urlPath = strings.TrimPrefix(urlPath, cfg.Prefix) }
		if urlPath == "" { urlPath = "/" }
		urlPath = filepath.Clean("/" + urlPath)
		if strings.Contains(urlPath, "..") { w.WriteHeader(403); return }
		fsPath := filepath.Join(cfg.Root, urlPath)
		info, err := os.Stat(fsPath)
		if err != nil {
			if os.IsNotExist(err) { w.WriteHeader(404); w.WriteString("404 Not Found\n") } else { w.WriteHeader(500) }
			return
		}
		if info.IsDir() {
			indexPath := filepath.Join(fsPath, cfg.Index)
			if indexInfo, err := os.Stat(indexPath); err == nil {
				serveFile(w, r, indexPath, indexInfo, cfg.CacheMaxAge); return
			}
			if cfg.Browse { serveDirListing(w, r, fsPath, urlPath); return }
			w.WriteHeader(403); return
		}
		serveFile(w, r, fsPath, info, cfg.CacheMaxAge)
	}
}

func serveFile(w parser.ResponseWriter, r *parser.Request, path string, info os.FileInfo, maxAge int) {
	etag := fmt.Sprintf(`"%x"`, md5.Sum([]byte(fmt.Sprintf("%d%d", info.Size(), info.ModTime().UnixNano()))))
	if r.Headers.Get("If-None-Match") == etag { w.WriteHeader(304); return }
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(time.RFC1123))
	if maxAge > 0 { w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge)) }
	if err := w.File(path); err != nil { w.WriteHeader(500) }
}

func serveDirListing(w parser.ResponseWriter, r *parser.Request, dir, urlPath string) {
	entries, err := os.ReadDir(dir)
	if err != nil { w.WriteHeader(500); return }
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Index of %s</title>
<style>body{font-family:monospace;padding:2rem;background:#1a1a2e;color:#e0e0e0}
h1{color:#00d2ff}table{width:100%%;border-collapse:collapse}
th,td{padding:.3rem .5rem;text-align:left}th{color:#aaa;border-bottom:1px solid #333}
a{color:#00d2ff;text-decoration:none}.dir{color:#ffd700}.sz{color:#888;text-align:right}
</style></head><body><h1>Index of %s</h1><table><tr><th>Name</th><th>Modified</th><th class=sz>Size</th></tr>`, urlPath, urlPath))
	if urlPath != "/" {
		sb.WriteString(fmt.Sprintf(`<tr><td><a href="%s">../</a></td><td></td><td></td></tr>`, filepath.Dir(urlPath)))
	}
	for _, e := range entries {
		name := e.Name()
		info, _ := e.Info()
		link := filepath.Join(urlPath, name)
		cls, size, mod := "", "", ""
		if e.IsDir() { name += "/"; cls = `class="dir"` } else if info != nil { size = formatSize(info.Size()) }
		if info != nil { mod = info.ModTime().Format("2006-01-02 15:04") }
		sb.WriteString(fmt.Sprintf(`<tr><td %s><a href="%s">%s</a></td><td>%s</td><td class=sz>%s</td></tr>`, cls, link, name, mod, size))
	}
	sb.WriteString("</table></body></html>")
	w.HTML(200, sb.String())
}

func formatSize(n int64) string {
	switch {
	case n >= 1<<30: return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20: return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10: return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default: return fmt.Sprintf("%dB", n)
	}
}

func Single(filePath string) router.HandlerFunc {
	return func(w parser.ResponseWriter, r *parser.Request) {
		info, err := os.Stat(filePath)
		if err != nil { w.WriteHeader(404); return }
		serveFile(w, r, filePath, info, 3600)
	}
}

func SPA(root, index string) router.HandlerFunc {
	if index == "" { index = "index.html" }
	return func(w parser.ResponseWriter, r *parser.Request) {
		fsPath := filepath.Join(root, filepath.Clean("/"+r.Path))
		if info, err := os.Stat(fsPath); err == nil && !info.IsDir() {
			serveFile(w, r, fsPath, info, 3600); return
		}
		indexPath := filepath.Join(root, index)
		info, err := os.Stat(indexPath)
		if err != nil { w.WriteHeader(404); return }
		serveFile(w, r, indexPath, info, 0)
	}
}

func detectContentType(path string) string {
	switch {
	case strings.HasSuffix(path, ".html"): return "text/html; charset=utf-8"
	case strings.HasSuffix(path, ".css"): return "text/css"
	case strings.HasSuffix(path, ".js"): return "application/javascript"
	case strings.HasSuffix(path, ".json"): return "application/json"
	case strings.HasSuffix(path, ".png"): return "image/png"
	case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".jpeg"): return "image/jpeg"
	case strings.HasSuffix(path, ".svg"): return "image/svg+xml"
	default: return "application/octet-stream"
	}
}
