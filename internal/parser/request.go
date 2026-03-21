package parser

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

var validMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true,
	"PATCH": true, "HEAD": true, "OPTIONS": true, "TRACE": true,
	"CONNECT": true,
}

var (
	ErrInvalidMethod      = errors.New("método HTTP inválido")
	ErrInvalidRequestLine = errors.New("request line inválida")
	ErrInvalidVersion     = errors.New("versão HTTP inválida")
	ErrHeaderTooLarge     = errors.New("header muito grande")
	ErrBodyTooLarge       = errors.New("body muito grande")
	ErrInvalidChunked     = errors.New("encoding chunked inválido")
)

const (
	MaxHeaderSize = 8 * 1024
	MaxBodySize   = 32 * 1024 * 1024
	MaxHeaders    = 100
)

// Request representa uma requisição HTTP/1.1 parseada do zero
type Request struct {
	Method      string
	Path        string
	RawQuery    string
	Fragment    string
	Version     string
	Headers     Headers
	Body        []byte
	RemoteAddr  string
	Conn        net.Conn
	Host        string
	ContentType string
	ContentLen  int64
	IsKeepAlive bool
	IsChunked   bool
	Params      map[string]string
	Query       map[string][]string
}

// Headers é um mapa case-insensitive
type Headers map[string][]string

func (h Headers) Get(key string) string {
	vals := h[canonicalHeader(key)]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func (h Headers) GetAll(key string) []string { return h[canonicalHeader(key)] }

func (h Headers) Set(key, val string) { h[canonicalHeader(key)] = []string{val} }

func (h Headers) Add(key, val string) {
	k := canonicalHeader(key)
	h[k] = append(h[k], val)
}

func (h Headers) Has(key string) bool {
	_, ok := h[canonicalHeader(key)]
	return ok
}

func canonicalHeader(s string) string {
	parts := strings.Split(strings.ToLower(s), "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "-")
}

// Parser lê e parseia requisições HTTP/1.1 de uma conexão TCP
type Parser struct {
	conn   net.Conn
	reader *bufio.Reader
}

func New(conn net.Conn) *Parser {
	return &Parser{conn: conn, reader: bufio.NewReaderSize(conn, 4096)}
}

func (p *Parser) ParseRequest() (*Request, error) {
	req := &Request{
		Headers:    make(Headers),
		Params:     make(map[string]string),
		Query:      make(map[string][]string),
		RemoteAddr: p.conn.RemoteAddr().String(),
		Conn:       p.conn,
	}
	if err := p.parseRequestLine(req); err != nil {
		return nil, err
	}
	if err := p.parseHeaders(req); err != nil {
		return nil, err
	}
	p.deriveFields(req)
	if err := p.parseBody(req); err != nil {
		return nil, err
	}
	return req, nil
}

func (p *Parser) parseRequestLine(req *Request) error {
	line, err := p.readLine()
	if err != nil {
		return fmt.Errorf("lendo request line: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line, err = p.readLine()
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 {
		return fmt.Errorf("%w: %q", ErrInvalidRequestLine, line)
	}
	if !validMethods[parts[0]] {
		return fmt.Errorf("%w: %s", ErrInvalidMethod, parts[0])
	}
	req.Method = parts[0]
	if err := parseURI(req, parts[1]); err != nil {
		return err
	}
	if parts[2] != "HTTP/1.0" && parts[2] != "HTTP/1.1" {
		return fmt.Errorf("%w: %s", ErrInvalidVersion, parts[2])
	}
	req.Version = parts[2]
	return nil
}

func parseURI(req *Request, raw string) error {
	if raw == "*" {
		req.Path = "*"
		return nil
	}
	if idx := strings.IndexByte(raw, '#'); idx >= 0 {
		req.Fragment = raw[idx+1:]
		raw = raw[:idx]
	}
	if idx := strings.IndexByte(raw, '?'); idx >= 0 {
		req.RawQuery = raw[idx+1:]
		raw = raw[:idx]
		req.Query = parseQueryString(req.RawQuery)
	}
	req.Path = raw
	if req.Path == "" {
		req.Path = "/"
	}
	return nil
}

func parseQueryString(raw string) map[string][]string {
	result := make(map[string][]string)
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		key := urlDecode(kv[0])
		val := ""
		if len(kv) == 2 {
			val = urlDecode(kv[1])
		}
		result[key] = append(result[key], val)
	}
	return result
}

func urlDecode(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	var result strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				result.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		result.WriteByte(s[i])
	}
	return result.String()
}

func (p *Parser) parseHeaders(req *Request) error {
	totalSize, count := 0, 0
	for {
		line, err := p.readLine()
		if err != nil {
			return fmt.Errorf("lendo headers: %w", err)
		}
		if line == "" {
			break
		}
		totalSize += len(line)
		if totalSize > MaxHeaderSize {
			return ErrHeaderTooLarge
		}
		count++
		if count > MaxHeaders {
			return ErrHeaderTooLarge
		}
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// Header multi-linha
		for {
			next, err := p.reader.ReadByte()
			if err != nil {
				break
			}
			if next != ' ' && next != '\t' {
				p.reader.UnreadByte()
				break
			}
			cont, _ := p.readLine()
			val += " " + strings.TrimSpace(cont)
		}
		req.Headers.Add(key, val)
	}
	return nil
}

func (p *Parser) deriveFields(req *Request) {
	req.Host = req.Headers.Get("Host")
	req.ContentType = req.Headers.Get("Content-Type")
	if cl := req.Headers.Get("Content-Length"); cl != "" {
		req.ContentLen, _ = strconv.ParseInt(cl, 10, 64)
	}
	te := strings.ToLower(req.Headers.Get("Transfer-Encoding"))
	req.IsChunked = strings.Contains(te, "chunked")
	conn := strings.ToLower(req.Headers.Get("Connection"))
	if req.Version == "HTTP/1.1" {
		req.IsKeepAlive = !strings.Contains(conn, "close")
	} else {
		req.IsKeepAlive = strings.Contains(conn, "keep-alive")
	}
}

func (p *Parser) parseBody(req *Request) error {
	if req.IsChunked {
		return p.readChunked(req)
	}
	if req.ContentLen <= 0 {
		return nil
	}
	if req.ContentLen > MaxBodySize {
		return ErrBodyTooLarge
	}
	buf := make([]byte, req.ContentLen)
	if _, err := io.ReadFull(p.reader, buf); err != nil {
		return fmt.Errorf("lendo body: %w", err)
	}
	req.Body = buf
	return nil
}

func (p *Parser) readChunked(req *Request) error {
	var body []byte
	totalSize := 0
	for {
		line, err := p.readLine()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidChunked, err)
		}
		if idx := strings.IndexByte(line, ';'); idx >= 0 {
			line = line[:idx]
		}
		size, err := strconv.ParseInt(strings.TrimSpace(line), 16, 64)
		if err != nil {
			return fmt.Errorf("%w: tamanho inválido %q", ErrInvalidChunked, line)
		}
		if size == 0 {
			p.readLine()
			break
		}
		totalSize += int(size)
		if totalSize > MaxBodySize {
			return ErrBodyTooLarge
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(p.reader, chunk); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidChunked, err)
		}
		body = append(body, chunk...)
		p.readLine()
	}
	req.Body = body
	req.ContentLen = int64(len(body))
	return nil
}

func (p *Parser) readLine() (string, error) {
	line, err := p.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (p *Parser) Reader() *bufio.Reader { return p.reader }
