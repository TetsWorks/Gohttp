package websocket

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/TetsWorks/Gohttp/internal/parser"
	"github.com/TetsWorks/Gohttp/internal/router"
)

const (
	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
	wsGUID         = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	MaxMessageSize = 16 * 1024 * 1024
)

var (
	ErrNotWebSocket    = errors.New("não é uma requisição WebSocket")
	ErrHandshakeFailed = errors.New("handshake WebSocket falhou")
	ErrConnClosed      = errors.New("conexão WebSocket fechada")
	ErrMessageTooLarge = errors.New("mensagem WebSocket muito grande")
)

type MessageType int

const (
	TextMessage   MessageType = 1
	BinaryMessage MessageType = 2
	CloseMessage  MessageType = 8
	PingMessage   MessageType = 9
	PongMessage   MessageType = 10
)

type Message struct {
	Type MessageType
	Data []byte
}

type Conn struct {
	conn           net.Conn
	reader         *bufio.Reader
	mu             sync.Mutex
	closed         bool
	closeCh        chan struct{}
	PingInterval   time.Duration
	MaxMessageSize int64
}

func newConn(c net.Conn, br *bufio.Reader) *Conn {
	return &Conn{conn: c, reader: br, closeCh: make(chan struct{}),
		PingInterval: 30 * time.Second, MaxMessageSize: MaxMessageSize}
}

func (c *Conn) ReadMessage() (*Message, error) {
	for {
		msg, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch msg.Type {
		case PingMessage:
			c.WriteMessage(PongMessage, msg.Data)
			continue
		case PongMessage:
			continue
		case CloseMessage:
			c.sendClose(1000, "")
			return msg, ErrConnClosed
		}
		return msg, nil
	}
}

func (c *Conn) readFrame() (*Message, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.reader, header); err != nil {
		return nil, err
	}
	opcode := header[0] & 0x0F
	masked := (header[1] >> 7) & 1
	payloadLen := int64(header[1] & 0x7F)
	switch payloadLen {
	case 126:
		var extLen uint16
		binary.Read(c.reader, binary.BigEndian, &extLen)
		payloadLen = int64(extLen)
	case 127:
		var extLen uint64
		binary.Read(c.reader, binary.BigEndian, &extLen)
		payloadLen = int64(extLen)
	}
	if payloadLen > c.MaxMessageSize {
		return nil, ErrMessageTooLarge
	}
	var maskKey [4]byte
	if masked == 1 {
		io.ReadFull(c.reader, maskKey[:])
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return nil, err
	}
	if masked == 1 {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return &Message{Type: MessageType(opcode), Data: payload}, nil
}

func (c *Conn) WriteMessage(msgType MessageType, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrConnClosed
	}
	return c.writeFrame(byte(msgType), data)
}

func (c *Conn) WriteText(s string) error  { return c.WriteMessage(TextMessage, []byte(s)) }
func (c *Conn) WriteBinary(b []byte) error { return c.WriteMessage(BinaryMessage, b) }

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	var header []byte
	n := len(payload)
	header = append(header, 0x80|opcode)
	switch {
	case n <= 125:
		header = append(header, byte(n))
	case n <= 65535:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127)
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(n))
		header = append(header, b...)
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if n > 0 {
		_, err := c.conn.Write(payload)
		return err
	}
	return nil
}

func (c *Conn) sendClose(code int, reason string) {
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	copy(payload[2:], reason)
	c.writeFrame(OpClose, payload)
}

func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.sendClose(1000, "normal closure")
	close(c.closeCh)
	return c.conn.Close()
}

func (c *Conn) RemoteAddr() string { return c.conn.RemoteAddr().String() }

func (c *Conn) StartPing() {
	if c.PingInterval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(c.PingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				b := make([]byte, 4)
				rand.Read(b)
				if err := c.WriteMessage(PingMessage, b); err != nil {
					return
				}
			case <-c.closeCh:
				return
			}
		}
	}()
}

type Upgrader struct {
	CheckOrigin func(r *parser.Request) bool
}

func (u *Upgrader) Upgrade(w parser.ResponseWriter, r *parser.Request) (*Conn, error) {
	if !isWebSocketRequest(r) {
		return nil, ErrNotWebSocket
	}
	if u.CheckOrigin != nil && !u.CheckOrigin(r) {
		w.WriteHeader(403)
		return nil, ErrHandshakeFailed
	}
	key := r.Headers.Get("Sec-Websocket-Key")
	if key == "" {
		key = r.Headers.Get("Sec-WebSocket-Key")
	}
	if key == "" {
		return nil, fmt.Errorf("%w: chave ausente", ErrHandshakeFailed)
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		computeAcceptKey(key) + "\r\n\r\n"
	if _, err := r.Conn.Write([]byte(resp)); err != nil {
		return nil, err
	}
	return newConn(r.Conn, bufio.NewReader(r.Conn)), nil
}

func isWebSocketRequest(r *parser.Request) bool {
	return strings.ToLower(r.Headers.Get("Upgrade")) == "websocket" &&
		strings.Contains(strings.ToLower(r.Headers.Get("Connection")), "upgrade")
}

func computeAcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

type Hub struct {
	mu      sync.RWMutex
	clients map[string]*Conn
	rooms   map[string]map[string]*Conn
}

func NewHub() *Hub {
	return &Hub{clients: make(map[string]*Conn), rooms: make(map[string]map[string]*Conn)}
}

func (h *Hub) Register(id string, conn *Conn) {
	h.mu.Lock(); defer h.mu.Unlock()
	h.clients[id] = conn
}

func (h *Hub) Unregister(id string) {
	h.mu.Lock(); defer h.mu.Unlock()
	delete(h.clients, id)
	for room, members := range h.rooms {
		delete(members, id)
		if len(members) == 0 { delete(h.rooms, room) }
	}
}

func (h *Hub) JoinRoom(room, id string) {
	h.mu.Lock(); defer h.mu.Unlock()
	if h.rooms[room] == nil { h.rooms[room] = make(map[string]*Conn) }
	if conn, ok := h.clients[id]; ok { h.rooms[room][id] = conn }
}

func (h *Hub) Broadcast(t MessageType, data []byte) {
	h.mu.RLock(); defer h.mu.RUnlock()
	for _, c := range h.clients { c.WriteMessage(t, data) }
}

func (h *Hub) BroadcastRoom(room string, t MessageType, data []byte) {
	h.mu.RLock(); defer h.mu.RUnlock()
	for _, c := range h.rooms[room] { c.WriteMessage(t, data) }
}

func (h *Hub) Count() int {
	h.mu.RLock(); defer h.mu.RUnlock()
	return len(h.clients)
}

var DefaultUpgrader = &Upgrader{CheckOrigin: func(r *parser.Request) bool { return true }}

type HandleFunc func(conn *Conn, r *parser.Request)

func Handler(fn HandleFunc) router.HandlerFunc {
	return func(w parser.ResponseWriter, r *parser.Request) {
		conn, err := DefaultUpgrader.Upgrade(w, r)
		if err != nil {
			w.WriteHeader(400)
			w.WriteString("WebSocket upgrade failed: " + err.Error())
			return
		}
		fn(conn, r)
	}
}
