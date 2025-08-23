package main

import (
	"context"
	"encoding/binary"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/kataras/iris/v12"
	"github.com/kataras/iris/v12/websocket"
	protox "github.com/nathonNot/go-gdt/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

const (
	// 你的业务二进制帧：code(4B) | len(4B) | payload
	headerSize = 8
	// 可根据需要改小端/大端；客户端也要一致
	byteOrder = binary.BigEndian
	// 连接参数
	pongWait   = 75 * time.Second
	pingPeriod = 45 * time.Second
	writeWait  = 10 * time.Second
)

// 原子布尔
type atomicBool struct{ v uint32 }

func (a *atomicBool) Load() bool       { return (*(*uint32)(unsafe.Pointer(&a.v))) == 1 }
func (a *atomicBool) Swap(b bool) bool { old := a.Load(); a.Store(b); return old }
func (a *atomicBool) Store(b bool) {
	if b {
		*(*uint32)(unsafe.Pointer(&a.v)) = 1
	} else {
		*(*uint32)(unsafe.Pointer(&a.v)) = 0
	}
}

// ConnWrap 封装 iris websocket 连接，提供串行化写入
type ConnWrap struct {
	c     *websocket.NSConn
	mu    sync.Mutex
	alive atomicBool
}

func (cw *ConnWrap) WriteBinary(b []byte) error {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if !cw.alive.Load() {
		return errors.New("connection closed")
	}
	// iris 的 ws 写入已经是非阻塞 API；这里再做超时保护可选
	return cw.c.Conn.Write(websocket.BinaryMessage, b)
}

func (cw *ConnWrap) Close() {
	if cw.alive.Swap(false) {
		_ = cw.c.Conn.Write(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"))
		_ = cw.c.Conn.Close()
	}
}

// ---- 编解码 ----

func makeFrame(code uint32, payload []byte) []byte {
	buf := make([]byte, headerSize+len(payload))
	byteOrder.PutUint32(buf[0:4], code)
	byteOrder.PutUint32(buf[4:8], uint32(len(payload)))
	copy(buf[8:], payload)
	return buf
}

func parseFrame(b []byte) (code uint32, payload []byte, err error) {
	if len(b) < headerSize {
		return 0, nil, errors.New("short frame")
	}
	code = byteOrder.Uint32(b[0:4])
	n := byteOrder.Uint32(b[4:8])
	if headerSize+int(n) != len(b) {
		return 0, nil, errors.New("invalid length")
	}
	return code, b[8:], nil
}

func main() {
	app := iris.New()
	app.Logger().SetLevel("info")

	// 1) 连接后端 gRPC（一个端口里挂多个服务也可以）
	cc, err := grpc.Dial("127.0.0.1:9090", grpc.WithInsecure())
	if err != nil {
		log.Fatalf("grpc dial: %v", err)
	}
	// 2) 构造 GatewaySet 并注册到 Router（多个服务就多次 RegisterXXXGateway）
	router := protox.NewRouter()
	protox.RegisterGameGateway(router, protox.NewGameGatewaySet(cc))
	// friendpb.RegisterFriendGateway(router, friendpb.NewFriendGatewaySet(cc))
	// guildpb.RegisterGuildGateway(router, guildpb.NewGuildGatewaySet(cc))

	// 3) WS 服务端
	ws := websocket.New(websocket.DefaultGorillaUpgrader, websocket.Events{
		websocket.OnNativeMessage: func(ns *websocket.NSConn, msg websocket.Message) error {
			// 仅处理 binary
			if !msg.IsNative {
				return nil
			}
			code, body, err := parseFrame(msg.Body)
			if err != nil {
				ns.Conn.Write(websocket.TextMessage, []byte("bad frame"))
				return nil
			}
			// 可按 code 做限流、鉴权、超时等
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			respCode, out, err := router.Dispatch(ctx, code, body)
			if err != nil {
				// 这里你可以定义统一错误帧；演示用文本
				ns.Conn.Write(websocket.TextMessage, []byte("rpc error: "+err.Error()))
				return nil
			}
			return ns.Conn.Write(websocket.BinaryMessage, makeFrame(respCode, out))
		},
		websocket.OnConnected: func(c *websocket.NSConn) error {
			c.Conn.SetReadLimit(4 << 20) // 4MB 限制
			c.Conn.SetReadDeadline(time.Now().Add(pongWait))
			c.Conn.SetPongHandler(func(appData string) error {
				c.Conn.SetReadDeadline(time.Now().Add(pongWait))
				return nil
			})
			// 心跳 goroutine
			go func() {
				ticker := time.NewTicker(pingPeriod)
				defer ticker.Stop()
				for range ticker.C {
					if err := c.Conn.Write(websocket.PingMessage, nil); err != nil {
						_ = c.Conn.Close()
						return
					}
				}
			}()
			return nil
		},
		websocket.OnError: func(ns *websocket.NSConn, err error) {
			log.Printf("ws error: %v", err)
		},
	})

	app.Get("/ws", websocket.Handler(ws))

	// 4) 健康检查
	app.Get("/healthz", func(ctx iris.Context) { ctx.WriteString("ok") })

	// 5) 启动 & 优雅退出
	srv := &http.Server{Addr: ":7000", Handler: app}
	go func() {
		log.Println("WS gateway on :7000 (path /ws)")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	_ = cc.Close()
}
