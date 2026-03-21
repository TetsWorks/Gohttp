package router

import (
	"strings"

	"github.com/TetsWorks/Gohttp/internal/parser"
)

// HandlerFunc é a assinatura de um handler de rota
type HandlerFunc func(w parser.ResponseWriter, r *parser.Request)

// Middleware é uma função que envolve um handler
type Middleware func(HandlerFunc) HandlerFunc

// node é um nó da trie de roteamento
type node struct {
	segment  string            // segmento literal ou ":param" ou "*"
	isParam  bool              // é parâmetro dinâmico?
	isWild   bool              // é wildcard?
	paramName string           // nome do parâmetro (sem :)
	children []*node
	handlers map[string]HandlerFunc // method -> handler
}

func newNode(segment string) *node {
	n := &node{
		segment:  segment,
		handlers: make(map[string]HandlerFunc),
	}
	if strings.HasPrefix(segment, ":") {
		n.isParam = true
		n.paramName = segment[1:]
	} else if segment == "*" || strings.HasPrefix(segment, "*") {
		n.isWild = true
		n.paramName = "wildcard"
		if len(segment) > 1 {
			n.paramName = segment[1:]
		}
	}
	return n
}

// Trie é a árvore de roteamento
type Trie struct {
	root *node
}

func newTrie() *Trie {
	return &Trie{root: newNode("/")}
}

func (t *Trie) insert(method, path string, handler HandlerFunc) {
	segments := splitPath(path)
	current := t.root

	for _, seg := range segments {
		found := false
		for _, child := range current.children {
			if child.segment == seg {
				current = child
				found = true
				break
			}
		}
		if !found {
			child := newNode(seg)
			current.children = append(current.children, child)
			current = child
		}
	}

	current.handlers[strings.ToUpper(method)] = handler
}

// Match encontra o handler e extrai params para o path e método dados
func (t *Trie) match(method, path string) (HandlerFunc, map[string]string, bool) {
	params := make(map[string]string)
	handler, ok := t.root.match(splitPath(path), method, params)
	return handler, params, ok
}

func (n *node) match(segments []string, method string, params map[string]string) (HandlerFunc, bool) {
	// Chegou no fim do path
	if len(segments) == 0 {
		h, ok := n.handlers[method]
		return h, ok
	}

	seg := segments[0]
	rest := segments[1:]

	for _, child := range n.children {
		// Wildcard consome tudo
		if child.isWild {
			params[child.paramName] = strings.Join(segments, "/")
			h, ok := child.handlers[method]
			return h, ok
		}
		// Parâmetro dinâmico
		if child.isParam {
			params[child.paramName] = seg
			if h, ok := child.match(rest, method, params); ok {
				return h, true
			}
			delete(params, child.paramName)
			continue
		}
		// Literal
		if child.segment == seg {
			if h, ok := child.match(rest, method, params); ok {
				return h, true
			}
		}
	}
	return nil, false
}

func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return []string{}
	}
	return strings.Split(path, "/")
}

// ─── Router ───────────────────────────────────────────────────────────────────

// Router gerencia rotas e grupos com middlewares
type Router struct {
	trie        *Trie
	middlewares []Middleware
	prefix      string
	notFound    HandlerFunc
	methodNotAllowed HandlerFunc
	panicHandler func(w parser.ResponseWriter, r *parser.Request, err interface{})
}

// New cria um novo Router
func New() *Router {
	return &Router{
		trie: newTrie(),
		notFound: func(w parser.ResponseWriter, r *parser.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(404)
			w.WriteString("404 Not Found\n")
		},
		methodNotAllowed: func(w parser.ResponseWriter, r *parser.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(405)
			w.WriteString("405 Method Not Allowed\n")
		},
	}
}

// Use adiciona middlewares globais
func (r *Router) Use(mws ...Middleware) {
	r.middlewares = append(r.middlewares, mws...)
}

// GET registra rota GET
func (r *Router) GET(path string, handler HandlerFunc, mws ...Middleware) {
	r.handle("GET", path, handler, mws...)
}

// POST registra rota POST
func (r *Router) POST(path string, handler HandlerFunc, mws ...Middleware) {
	r.handle("POST", path, handler, mws...)
}

// PUT registra rota PUT
func (r *Router) PUT(path string, handler HandlerFunc, mws ...Middleware) {
	r.handle("PUT", path, handler, mws...)
}

// DELETE registra rota DELETE
func (r *Router) DELETE(path string, handler HandlerFunc, mws ...Middleware) {
	r.handle("DELETE", path, handler, mws...)
}

// PATCH registra rota PATCH
func (r *Router) PATCH(path string, handler HandlerFunc, mws ...Middleware) {
	r.handle("PATCH", path, handler, mws...)
}

// HEAD registra rota HEAD
func (r *Router) HEAD(path string, handler HandlerFunc, mws ...Middleware) {
	r.handle("HEAD", path, handler, mws...)
}

// OPTIONS registra rota OPTIONS
func (r *Router) OPTIONS(path string, handler HandlerFunc, mws ...Middleware) {
	r.handle("OPTIONS", path, handler, mws...)
}

// ANY registra rota para todos os métodos
func (r *Router) ANY(path string, handler HandlerFunc, mws ...Middleware) {
	for _, m := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
		r.handle(m, path, handler, mws...)
	}
}

func (r *Router) handle(method, path string, handler HandlerFunc, mws ...Middleware) {
	fullPath := r.prefix + path
	// Aplica middlewares da rota + globais (ordem: globais primeiro, rota depois)
	h := handler
	// Middlewares da rota (aplicados de dentro pra fora)
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	// Middlewares globais
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		h = r.middlewares[i](h)
	}
	r.trie.insert(method, fullPath, h)
}

// Group cria um grupo de rotas com prefixo e middlewares comuns
func (r *Router) Group(prefix string, mws ...Middleware) *Group {
	return &Group{
		router:      r,
		prefix:      r.prefix + prefix,
		middlewares: append(append([]Middleware{}, r.middlewares...), mws...),
	}
}

// NotFound define handler para 404
func (r *Router) NotFound(h HandlerFunc) { r.notFound = h }

// MethodNotAllowed define handler para 405
func (r *Router) MethodNotAllowed(h HandlerFunc) { r.methodNotAllowed = h }

// PanicHandler define handler para panics
func (r *Router) PanicHandler(h func(w parser.ResponseWriter, r *parser.Request, err interface{})) {
	r.panicHandler = h
}

// ServeHTTP roteia a requisição
func (r *Router) ServeHTTP(w parser.ResponseWriter, req *parser.Request) {
	// Recover de panics
	if r.panicHandler != nil {
		defer func() {
			if err := recover(); err != nil {
				r.panicHandler(w, req, err)
			}
		}()
	}

	handler, params, found := r.trie.match(req.Method, req.Path)
	if !found {
		// Checa se há handler para outro método (405)
		for _, m := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
			if m == req.Method {
				continue
			}
			if h, _, ok := r.trie.match(m, req.Path); ok && h != nil {
				r.methodNotAllowed(w, req)
				return
			}
		}
		r.notFound(w, req)
		return
	}

	// Injeta params na request
	for k, v := range params {
		req.Params[k] = v
	}

	handler(w, req)
}

// ─── Group ────────────────────────────────────────────────────────────────────

// Group é um grupo de rotas com prefixo comum
type Group struct {
	router      *Router
	prefix      string
	middlewares []Middleware
}

func (g *Group) Use(mws ...Middleware) {
	g.middlewares = append(g.middlewares, mws...)
}

func (g *Group) GET(path string, h HandlerFunc, mws ...Middleware) {
	g.handle("GET", path, h, mws...)
}

func (g *Group) POST(path string, h HandlerFunc, mws ...Middleware) {
	g.handle("POST", path, h, mws...)
}

func (g *Group) PUT(path string, h HandlerFunc, mws ...Middleware) {
	g.handle("PUT", path, h, mws...)
}

func (g *Group) DELETE(path string, h HandlerFunc, mws ...Middleware) {
	g.handle("DELETE", path, h, mws...)
}

func (g *Group) PATCH(path string, h HandlerFunc, mws ...Middleware) {
	g.handle("PATCH", path, h, mws...)
}

func (g *Group) Group(prefix string, mws ...Middleware) *Group {
	return &Group{
		router:      g.router,
		prefix:      g.prefix + prefix,
		middlewares: append(append([]Middleware{}, g.middlewares...), mws...),
	}
}

func (g *Group) handle(method, path string, handler HandlerFunc, mws ...Middleware) {
	fullPath := g.prefix + path
	h := handler
	allMws := append(append([]Middleware{}, mws...), g.middlewares...)
	for i := len(allMws) - 1; i >= 0; i-- {
		h = allMws[i](h)
	}
	g.router.trie.insert(method, fullPath, h)
}
