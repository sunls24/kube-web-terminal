package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"io"
	command "kube-web-terminal/command"
	"net/http"
	"os"
	"strings"
	"time"
)

func init() {
	// 日志打印格式
	format := new(log.TextFormatter)
	format.FullTimestamp = true
	format.TimestampFormat = "06-01-02 15:04:05"
	log.SetFormatter(format)
}

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Maximum message size allowed from peer.
	maxMessageSize = 8192

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10
)

const (
	msgCommand = '0'
	msgResize  = '1'
)

func pumpStdin(ws *websocket.Conn, w io.Writer) {
	ws.SetReadLimit(maxMessageSize)
	_ = ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error { _ = ws.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			break
		}
		switch message[0] {
		case msgCommand:
			message = message[1:]
		case msgResize:
			s := &command.Size{}
			err = json.Unmarshal(message[1:], s)
			if err != nil {
				_ = ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("resize: json.Unmarshal: %v", err)))
				continue
			}

			ptyRW := w.(*os.File)
			size, err := pty.GetsizeFull(ptyRW)
			if err != nil {
				_ = ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("resize: GetsizeFull: %v", err)))
			}
			size.Rows = s.Rows
			size.Cols = s.Cols
			err = pty.Setsize(ptyRW, size)
			if err != nil {
				_ = ws.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("resize: Setsize: %v", err)))
			}
			fmt.Println("resize done")
			continue
		default:
			continue
		}

		if _, err = w.Write(message); err != nil {
			break
		}
	}
}

func pumpStdout(ws *websocket.Conn, r io.Reader) {
	buff := make([]byte, 1024)
	for {
		n, err := r.Read(buff)
		if err != nil {
			break
		}
		_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
		if err := ws.WriteMessage(websocket.TextMessage, buff[:n]); err != nil {
			break
		}
	}

	_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
	_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

func ping(ws *websocket.Conn, quit chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
				log.Error(errors.Wrap(err, "ping"))
			}
		case <-quit:
			return
		}
	}
}

func internalError(ws *websocket.Conn, err error) {
	err = errors.Wrap(err, "internal server error")
	log.Error(err)
	_ = ws.WriteMessage(websocket.TextMessage, []byte(err.Error()))
}

func resize(ptyRW *os.File, cols, rows int) error {
	return pty.Setsize(ptyRW, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func serveWs(w http.ResponseWriter, r *http.Request) {
	podName := r.FormValue("podName")
	namespace := r.FormValue("namespace")
	container := r.FormValue("container")
	sh := r.FormValue("command")
	if len(sh) == 0 {
		sh = "sh"
	}
	if !strings.Contains(sh, "sh") {
		log.Error("command needs to be an sh-like command")
		return
	}
	if CheckEmpty(podName, namespace, container) {
		log.Error("[podName, namespace, container] cannot be empty")
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error(errors.Wrap(err, "upgrade ws"))
		return
	}
	defer ws.Close()

	dLog := log.WithField("podName", podName).WithField("namespace", namespace).WithField("container", container).WithField("command", sh)
	dLog.Info("start ws connect")

	// websocket读写pty，exec程序读写tty
	ptyRW, ttyRW, err := pty.Open()
	if err != nil {
		internalError(ws, errors.Wrap(err, "pty.Open"))
		return
	}
	defer ttyRW.Close()
	defer ptyRW.Close()

	_ = pty.Setsize(ptyRW, &pty.Winsize{Cols: 35, Rows: 24})

	cmd, err := command.NewCommand(ttyRW, []string{sh}, podName, namespace, container)
	if err != nil {
		internalError(ws, errors.Wrap(err, "NewCommand"))
		return
	}

	// stream 和 ws 有任意一个中断或结束，需要通知对方停止
	quit := make(chan struct{})
	var streamDone bool
	go func() {
		dLog.Info("stream start")
		err := cmd.Stream(quit)
		dLog.Info("stream end, error: ", err)
		streamDone = true
		_ = ptyRW.Close() // 关闭使 pumpStdout 停止
	}()

	go pumpStdout(ws, ptyRW)
	go ping(ws, quit)

	// ws 连接进行中会在此处堵塞
	pumpStdin(ws, ptyRW)

	close(quit)

	go func() {
		for i := 0; i < 5; i++ {
			if streamDone {
				break
			}
			// 尝试退出 shell
			_, _ = ptyRW.WriteString("exit\n")
			<-time.After(time.Second)
		}
		if !streamDone {
			dLog.Error("stream did not exit successfully")
		}
	}()

	dLog.Info("end ws connect")
}

func serveHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.ServeFile(w, r, "home.html")
}

func main() {
	addr := flag.String("addr", ":8081", "http service address")
	flag.Parse()

	http.HandleFunc("/", serveHome)
	http.HandleFunc("/ws", serveWs)
	log.Info("start http listen addr: ", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
