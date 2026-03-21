# GoHTTP

> Servidor HTTP/1.1 escrito do zero em Go — sem `net/http`, sem frameworks externos.

## Features

- **Parser HTTP/1.1 do zero** — request line, headers, body, chunked encoding, keep-alive
- **Trie Router** — parâmetros `/:id`, wildcards `/*path`, grupos com prefixo
- **Middleware chain** — logger colorido, CORS, rate limiter (token bucket), Basic/Bearer auth, recover, security headers
- **TLS/HTTPS** — certificados externos ou self-signed gerado automaticamente
- **WebSockets** — handshake RFC 6455 do zero, ping/pong, Hub pub/sub, rooms
- **Arquivos estáticos** — ETag, If-None-Match, cache headers, listagem de diretório, SPA fallback
- **Painel de métricas** — req/s, latência (histograma), erros por rota, dashboard HTML + JSON API

## Instalação (Termux)

```bash
pkg install golang git
git clone https://github.com/TetsWorks/Gohttp
cd Gohttp
go mod tidy
make termux
```

## Uso rápido

```go
srv := gohttp.New(":8080")

srv.GET("/", func(w parser.ResponseWriter, r *parser.Request) {
    w.JSON(200, map[string]string{"hello": "world"})
})

srv.GET("/users/:id", func(w parser.ResponseWriter, r *parser.Request) {
    w.JSON(200, map[string]string{"id": r.Params["id"]})
})

srv.Listen()
```

## Estrutura

```
GoHTTP/
├── cmd/gohttp/main.go         # Exemplo completo
├── internal/
│   ├── server.go              # Server principal + graceful shutdown
│   ├── tls_gen.go             # Gerador de cert self-signed
│   ├── parser/
│   │   ├── request.go         # Parser HTTP/1.1 do zero
│   │   └── response.go        # Serialização de response
│   ├── router/
│   │   └── router.go          # Trie router + grupos
│   ├── middleware/
│   │   └── middleware.go      # Logger, CORS, RateLimit, Auth...
│   ├── websocket/
│   │   └── websocket.go       # WebSocket RFC 6455 do zero
│   ├── static/
│   │   └── static.go          # Servidor de arquivos + SPA
│   └── metrics/
│       └── metrics.go         # Coleta + dashboard HTML
└── Makefile
```

## Endpoints de exemplo

| Método | Path | Descrição |
|---|---|---|
| GET | `/` | Demo HTML com chat WS |
| GET | `/ping` | Health check JSON |
| GET | `/users/:id` | Parâmetro de rota |
| GET | `/files/*path` | Wildcard |
| GET | `/api/v1/status` | Status com rate limit |
| POST | `/api/v1/echo` | Echo JSON |
| GET | `/admin/dashboard` | Basic Auth |
| GET | `/static/*` | Arquivos estáticos |
| GET | `/ws` | WebSocket |
| GET | `/_metrics` | Dashboard de métricas |

## Licença

MIT
