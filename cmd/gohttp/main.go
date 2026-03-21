package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	gohttp "github.com/TetsWorks/Gohttp/internal"
	"github.com/TetsWorks/Gohttp/internal/middleware"
	"github.com/TetsWorks/Gohttp/internal/parser"
	"github.com/TetsWorks/Gohttp/internal/router"
	"github.com/TetsWorks/Gohttp/internal/static"
	"github.com/TetsWorks/Gohttp/internal/websocket"
)

func main() {
	addr := ":8080"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	srv := gohttp.New(addr)

	// ─── Middlewares globais ─────────────────────────────────────────────────
	srv.Use(
		middleware.RequestID(),
		middleware.SecurityHeaders(),
		middleware.CORS(),
	)

	// ─── Rotas básicas ───────────────────────────────────────────────────────
	srv.GET("/", func(w parser.ResponseWriter, r *parser.Request) {
		w.HTML(200, indexHTML)
	})

	srv.GET("/ping", func(w parser.ResponseWriter, r *parser.Request) {
		w.JSON(200, map[string]interface{}{
			"status": "ok",
			"time":   time.Now().UTC(),
			"server": "GoHTTP/1.0",
		})
	})

	// ─── Parâmetros de rota ───────────────────────────────────────────────────
	srv.GET("/users/:id", func(w parser.ResponseWriter, r *parser.Request) {
		id := r.Params["id"]
		w.JSON(200, map[string]interface{}{
			"id":   id,
			"name": "Usuário " + id,
			"ts":   time.Now().Unix(),
		})
	})

	srv.GET("/files/*path", func(w parser.ResponseWriter, r *parser.Request) {
		w.JSON(200, map[string]string{
			"path":    r.Params["path"],
			"message": "wildcard funcionando!",
		})
	})

	// ─── Grupo com autenticação ───────────────────────────────────────────────
	api := srv.Group("/api/v1",
		middleware.RateLimit(middleware.DefaultRateLimiterConfig),
		middleware.NoCache(),
	)

	api.GET("/status", func(w parser.ResponseWriter, r *parser.Request) {
		w.JSON(200, map[string]interface{}{
			"status":  "running",
			"version": "1.0.0",
			"uptime":  time.Since(startTime).String(),
		})
	})

	api.POST("/echo", func(w parser.ResponseWriter, r *parser.Request) {
		var body interface{}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			w.JSON(400, map[string]string{"error": "JSON inválido"})
			return
		}
		w.JSON(200, map[string]interface{}{
			"echo":    body,
			"method":  r.Method,
			"path":    r.Path,
			"headers": r.Headers,
		})
	})

	// Grupo protegido com Basic Auth
	admin := srv.Group("/admin",
		middleware.BasicAuth("GoHTTP Admin", map[string]string{
			"admin": "senha123",
		}),
	)

	admin.GET("/dashboard", func(w parser.ResponseWriter, r *parser.Request) {
		w.JSON(200, map[string]string{
			"message": "Área administrativa",
			"user":    "admin",
		})
	})

	// ─── Arquivos estáticos ───────────────────────────────────────────────────
	staticHandler := static.Handler(static.Config{
		Root:        "./public",
		Browse:      true,
		CacheMaxAge: 3600,
		Prefix:      "/static",
	})
	srv.GET("/static", staticHandler)
	srv.GET("/static/", staticHandler)
	srv.GET("/static/*path", staticHandler)

	// ─── WebSocket ────────────────────────────────────────────────────────────
	hub := websocket.NewHub()

	srv.GET("/ws", websocket.Handler(func(conn *websocket.Conn, r *parser.Request) {
		id := fmt.Sprintf("%d", time.Now().UnixNano())
		hub.Register(id, conn)
		defer func() {
			hub.Unregister(id)
			conn.Close()
		}()

		conn.StartPing()
		log.Printf("WS: cliente %s conectado (%d total)", conn.RemoteAddr(), hub.Count())

		// Boas-vindas
		conn.WriteText(fmt.Sprintf(`{"type":"welcome","id":"%s","clients":%d}`, id, hub.Count()))

		for {
			msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			if msg.Type == websocket.TextMessage {
				// Broadcast para todos
				broadcast := fmt.Sprintf(`{"type":"message","from":"%s","data":%s}`, id, string(msg.Data))
				hub.Broadcast(websocket.TextMessage, []byte(broadcast))
			}
		}
		log.Printf("WS: cliente %s desconectado", conn.RemoteAddr())
	}))

	srv.GET("/ws/count", func(w parser.ResponseWriter, r *parser.Request) {
		w.JSON(200, map[string]int{"connections": hub.Count()})
	})

	// ─── Handler customizado para 404 ────────────────────────────────────────
	srv.Router.NotFound(func(w parser.ResponseWriter, r *parser.Request) {
		if r.Headers.Get("Accept") == "application/json" {
			w.JSON(404, map[string]string{
				"error": "rota não encontrada",
				"path":  r.Path,
			})
			return
		}
		w.HTML(404, fmt.Sprintf(`<!DOCTYPE html><html><head><title>404</title>
<style>body{background:#0f0f23;color:#e0e0ff;font-family:monospace;text-align:center;padding:5rem}
h1{font-size:5rem;color:#00d2ff}p{color:#888}</style></head>
<body><h1>404</h1><p>%s não encontrado</p><a href="/" style="color:#00d2ff">← voltar</a></body></html>`, r.Path))
	})

	// ─── Graceful shutdown ────────────────────────────────────────────────────
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		fmt.Println("\n\033[33mShutting down...\033[0m")
		srv.Shutdown()
		os.Exit(0)
	}()

	// Inicia servidor
	if err := srv.Listen(); err != nil {
		log.Fatal(err)
	}
}

var startTime = time.Now()

// ─── Handlers auxiliares ─────────────────────────────────────────────────────

func demoMiddleware(name string) router.Middleware {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(w parser.ResponseWriter, r *parser.Request) {
			w.Header().Set("X-Demo-"+name, "true")
			next(w, r)
		}
	}
}

const indexHTML = `<!DOCTYPE html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<title>GoHTTP — Demo</title>
<style>
:root{--bg:#0f0f23;--text:#e0e0ff;--blue:#00d2ff;--green:#00ff88;--muted:#888;--card:#1a1a35;--border:#2a2a4a}
*{box-sizing:border-box;margin:0;padding:0}
body{background:var(--bg);color:var(--text);font-family:'Segoe UI',monospace;padding:2rem;max-width:900px;margin:0 auto}
h1{font-size:2.5rem;background:linear-gradient(90deg,#00d2ff,#b44fff);-webkit-background-clip:text;-webkit-text-fill-color:transparent;margin-bottom:.5rem}
.sub{color:var(--muted);margin-bottom:2rem}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(250px,1fr));gap:1rem;margin-bottom:2rem}
.card{background:var(--card);border:1px solid var(--border);border-radius:8px;padding:1.2rem}
.card h3{color:var(--blue);margin-bottom:.8rem;font-size:.9rem;text-transform:uppercase;letter-spacing:.1em}
.endpoint{display:flex;align-items:center;gap:.5rem;margin:.3rem 0;font-size:.85rem}
.badge{padding:.15rem .4rem;border-radius:3px;font-size:.7rem;font-weight:700;min-width:55px;text-align:center}
.GET{background:#0d3d6e;color:#00d2ff}.POST{background:#0d4a1e;color:#00ff88}
.PUT{background:#4a3d00;color:#ffd700}.DELETE{background:#4a1010;color:#ff4444}
a{color:var(--blue);text-decoration:none}a:hover{text-decoration:underline}
.ws-box{background:var(--card);border:1px solid var(--border);border-radius:8px;padding:1.2rem;margin-bottom:1rem}
#ws-log{height:150px;overflow-y:auto;background:#111;border-radius:4px;padding:.5rem;font-size:.8rem;color:#aaa;margin:.5rem 0}
input{background:#111;border:1px solid var(--border);color:var(--text);padding:.4rem .8rem;border-radius:4px;width:calc(100%% - 80px)}
button{background:var(--blue);color:#000;border:none;padding:.4rem .8rem;border-radius:4px;cursor:pointer;font-weight:700}
.status{display:inline-block;width:8px;height:8px;border-radius:50%;background:#ff4444;margin-right:.3rem}
.status.connected{background:#00ff88}
</style>
</head>
<body>
<h1>⚡ GoHTTP</h1>
<p class=sub>Servidor HTTP/1.1 escrito do zero em Go • sem net/http</p>

<div class=grid>
<div class=card>
<h3>🔀 Roteamento</h3>
<div class=endpoint><span class="badge GET">GET</span><a href="/ping">/ping</a></div>
<div class=endpoint><span class="badge GET">GET</span><a href="/users/42">/users/:id</a></div>
<div class=endpoint><span class="badge GET">GET</span><a href="/files/foo/bar">/files/*path</a></div>
<div class=endpoint><span class="badge GET">GET</span><a href="/api/v1/status">/api/v1/status</a></div>
<div class=endpoint><span class="badge POST">POST</span><span>/api/v1/echo</span></div>
</div>
<div class=card>
<h3>🔒 Auth & Segurança</h3>
<div class=endpoint><span class="badge GET">GET</span><a href="/admin/dashboard">/admin/dashboard</a></div>
<div style="color:var(--muted);font-size:.8rem;margin-top:.5rem">Basic Auth: admin / senha123</div>
</div>
<div class=card>
<h3>📁 Arquivos Estáticos</h3>
<div class=endpoint><span class="badge GET">GET</span><a href="/static/">/static/*</a></div>
<div style="color:var(--muted);font-size:.8rem;margin-top:.5rem">Browse + ETag + Cache</div>
</div>
<div class=card>
<h3>📊 Métricas</h3>
<div class=endpoint><span class="badge GET">GET</span><a href="/_metrics">/_metrics</a></div>
<div class=endpoint><span class="badge GET">GET</span><a href="/_metrics/json">/_metrics/json</a></div>
<div style="color:var(--muted);font-size:.8rem;margin-top:.5rem">Dashboard + JSON API</div>
</div>
</div>

<div class=ws-box>
<h3 style="color:var(--blue);margin-bottom:.8rem">⚡ WebSocket Chat Demo</h3>
<div><span class=status id=dot></span><span id=ws-status>Desconectado</span></div>
<div id=ws-log></div>
<div style="display:flex;gap:.5rem;margin-top:.5rem">
<input id=ws-input placeholder="Digite uma mensagem..." disabled>
<button id=ws-send disabled>Enviar</button>
</div>
<button id=ws-connect style="margin-top:.5rem;background:#00ff88">Conectar</button>
</div>

<script>
let ws=null;
const log=document.getElementById('ws-log');
const dot=document.getElementById('dot');
const status=document.getElementById('ws-status');
const input=document.getElementById('ws-input');
const send=document.getElementById('ws-send');
const conn=document.getElementById('ws-connect');

function addLog(msg,color='#aaa'){
  const d=document.createElement('div');
  d.style.color=color;
  d.textContent='['+new Date().toLocaleTimeString()+'] '+msg;
  log.appendChild(d);
  log.scrollTop=log.scrollHeight;
}

conn.onclick=()=>{
  if(ws){ws.close();return;}
  ws=new WebSocket('ws://'+location.host+'/ws');
  ws.onopen=()=>{
    dot.classList.add('connected');
    status.textContent='Conectado';
    input.disabled=false;send.disabled=false;
    conn.textContent='Desconectar';conn.style.background='#ff4444';
    addLog('Conectado ao servidor!','#00ff88');
  };
  ws.onmessage=e=>{
    try{const d=JSON.parse(e.data);
      if(d.type==='welcome')addLog('Boas-vindas! ID: '+d.id+' | Clients: '+d.clients,'#00d2ff');
      else if(d.type==='message')addLog(d.from.slice(-6)+': '+d.data,'#e0e0ff');
    }catch{addLog(e.data);}
  };
  ws.onclose=()=>{
    dot.classList.remove('connected');status.textContent='Desconectado';
    input.disabled=true;send.disabled=true;
    conn.textContent='Conectar';conn.style.background='#00ff88';
    ws=null;addLog('Desconectado','#ff4444');
  };
};

send.onclick=()=>{
  const msg=input.value.trim();
  if(msg&&ws){ws.send(JSON.stringify(msg));input.value='';}
};
input.onkeydown=e=>{if(e.key==='Enter')send.click();};
</script>
</body>
</html>`
