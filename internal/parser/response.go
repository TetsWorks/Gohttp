package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// StatusTexts mapeia status codes para mensagens
var StatusTexts = map[int]string{
	100: "Continue", 101: "Switching Protocols",
	200: "OK", 201: "Created", 202: "Accepted", 204: "No Content",
	206: "Partial Content",
	301: "Moved Permanently", 302: "Found", 304: "Not Modified",
	307: "Temporary Redirect", 308: "Permanent Redirect",
	400: "Bad Request", 401: "Unauthorized", 403: "Forbidden",
	404: "Not Found", 405: "Method Not Allowed", 408: "Request Timeout",
	409: "Conflict", 410: "Gone", 413: "Payload Too Large",
	415: "Unsupported Media Type", 422: "Unprocessable Entity",
	429: "Too Many Requests",
	500: "Internal Server Error", 501: "Not Implemented",
	502: "Bad Gateway", 503: "Service Unavailable", 504: "Gateway Timeout",
}

// ResponseWriter é a interface de escrita de respostas HTTP
type ResponseWriter interface {
	Header() Headers
	WriteHeader(statusCode int)
	Write(data []byte) (int, error)
	WriteString(s string) (int, error)
	JSON(statusCode int, v interface{}) error
	Text(statusCode int, s string) error
	HTML(statusCode int, s string) error
	Redirect(statusCode int, url string)
	File(path string) error
	Stream(statusCode int, contentType string, r io.Reader) error
	Flush() error
	Status() int
	Written() bool
}

// Response implementa ResponseWriter escrevendo diretamente na conexão TCP
type Response struct {
	conn        net.Conn
	writer      *bufio.Writer
	headers     Headers
	statusCode  int
	headersSent bool
	bytesWritten int64
	req         *Request
}

// NewResponse cria um novo Response para a conexão
func NewResponse(conn net.Conn, req *Request) *Response {
	return &Response{
		conn:    conn,
		writer:  bufio.NewWriterSize(conn, 4096),
		headers: make(Headers),
		req:     req,
	}
}

func (r *Response) Header() Headers { return r.headers }

func (r *Response) Status() int { return r.statusCode }

func (r *Response) Written() bool { return r.headersSent }

func (r *Response) WriteHeader(code int) {
	if r.headersSent {
		return
	}
	r.statusCode = code
	r.sendHeaders()
}

func (r *Response) sendHeaders() {
	if r.headersSent {
		return
	}
	r.headersSent = true

	if r.statusCode == 0 {
		r.statusCode = 200
	}

	statusText := StatusTexts[r.statusCode]
	if statusText == "" {
		statusText = "Unknown"
	}

	// Status line
	fmt.Fprintf(r.writer, "HTTP/1.1 %d %s\r\n", r.statusCode, statusText)

	// Headers padrão
	if r.headers.Get("Date") == "" {
		r.headers.Set("Date", time.Now().UTC().Format(http1TimeFormat))
	}
	if r.headers.Get("Server") == "" {
		r.headers.Set("Server", "GoHTTP/1.0")
	}
	if r.req != nil && !r.req.IsKeepAlive {
		r.headers.Set("Connection", "close")
	} else {
		r.headers.Set("Connection", "keep-alive")
	}

	// Escreve todos os headers
	for key, vals := range r.headers {
		for _, val := range vals {
			fmt.Fprintf(r.writer, "%s: %s\r\n", key, val)
		}
	}
	fmt.Fprintf(r.writer, "\r\n")
}

func (r *Response) Write(data []byte) (int, error) {
	if !r.headersSent {
		r.sendHeaders()
	}
	// HEAD não envia body
	if r.req != nil && r.req.Method == "HEAD" {
		return len(data), nil
	}
	n, err := r.writer.Write(data)
	r.bytesWritten += int64(n)
	return n, err
}

func (r *Response) WriteString(s string) (int, error) {
	return r.Write([]byte(s))
}

func (r *Response) JSON(statusCode int, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	r.headers.Set("Content-Type", "application/json; charset=utf-8")
	r.headers.Set("Content-Length", strconv.Itoa(len(data)))
	r.WriteHeader(statusCode)
	_, err = r.Write(data)
	return err
}

func (r *Response) Text(statusCode int, s string) error {
	data := []byte(s)
	r.headers.Set("Content-Type", "text/plain; charset=utf-8")
	r.headers.Set("Content-Length", strconv.Itoa(len(data)))
	r.WriteHeader(statusCode)
	_, err := r.Write(data)
	return err
}

func (r *Response) HTML(statusCode int, s string) error {
	data := []byte(s)
	r.headers.Set("Content-Type", "text/html; charset=utf-8")
	r.headers.Set("Content-Length", strconv.Itoa(len(data)))
	r.WriteHeader(statusCode)
	_, err := r.Write(data)
	return err
}

func (r *Response) Redirect(statusCode int, url string) {
	r.headers.Set("Location", url)
	r.headers.Set("Content-Length", "0")
	r.WriteHeader(statusCode)
}

func (r *Response) File(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	ct := detectContentType(path)
	r.headers.Set("Content-Type", ct)
	r.headers.Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	r.headers.Set("Last-Modified", info.ModTime().UTC().Format(http1TimeFormat))
	r.WriteHeader(200)

	if r.req != nil && r.req.Method == "HEAD" {
		return nil
	}

	_, err = io.Copy(r.writer, f)
	return err
}

func (r *Response) Stream(statusCode int, contentType string, reader io.Reader) error {
	r.headers.Set("Content-Type", contentType)
	r.headers.Set("Transfer-Encoding", "chunked")
	r.WriteHeader(statusCode)

	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			fmt.Fprintf(r.writer, "%x\r\n", n)
			r.writer.Write(buf[:n])
			fmt.Fprintf(r.writer, "\r\n")
		}
		if err == io.EOF {
			fmt.Fprintf(r.writer, "0\r\n\r\n")
			break
		}
		if err != nil {
			return err
		}
	}
	return r.writer.Flush()
}

func (r *Response) Flush() error {
	return r.writer.Flush()
}

func (r *Response) BytesWritten() int64 { return r.bytesWritten }

const http1TimeFormat = "Mon, 02 Jan 2006 15:04:05 GMT"

func detectContentType(path string) string {
	path = strings.ToLower(path)
	switch {
	case strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".htm"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(path, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(path, ".js"):
		return "application/javascript"
	case strings.HasSuffix(path, ".json"):
		return "application/json"
	case strings.HasSuffix(path, ".png"):
		return "image/png"
	case strings.HasSuffix(path, ".jpg") || strings.HasSuffix(path, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(path, ".gif"):
		return "image/gif"
	case strings.HasSuffix(path, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(path, ".ico"):
		return "image/x-icon"
	case strings.HasSuffix(path, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(path, ".xml"):
		return "application/xml"
	case strings.HasSuffix(path, ".txt"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(path, ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(path, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(path, ".wasm"):
		return "application/wasm"
	case strings.HasSuffix(path, ".gz"):
		return "application/gzip"
	case strings.HasSuffix(path, ".zip"):
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}
