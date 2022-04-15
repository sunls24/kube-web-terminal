package main

import (
	"encoding/json"
	"flag"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"io"
	"k8s.io/client-go/tools/remotecommand"
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

func pumpStdin(ws *websocket.Conn, w io.Writer, resize chan<- remotecommand.TerminalSize) {
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
			size := &command.Size{}
			if err = json.Unmarshal(message[1:], size); err != nil {
				continue
			}
			resize <- remotecommand.TerminalSize{Width: size.Cols, Height: size.Rows}
			continue
		default:
			continue
		}

		if _, err = w.Write(message); err != nil {
			break
		}
	}
}

func pumpStdout(ws *Conn, r io.Reader) {
	buff := make([]byte, 1024)
	for {
		n, err := r.Read(buff)
		if err != nil {
			break
		}
		_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
		if err := ws.WriteSafe(websocket.TextMessage, buff[:n]); err != nil {
			break
		}
	}

	_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
	_ = ws.WriteSafe(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
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

func internalError(ws *Conn, err error) {
	err = errors.Wrap(err, "internal error")
	log.Error(err)
	_ = ws.WriteSafe(websocket.TextMessage, []byte(err.Error()))
}

func serveWs(w http.ResponseWriter, r *http.Request) {
	ws, err := Upgrade(w, r)
	if err != nil {
		log.Error(errors.Wrap(err, "upgrade ws"))
		return
	}
	defer ws.Close()

	podName := r.FormValue("podName")
	namespace := r.FormValue("namespace")
	container := r.FormValue("container")
	sh := r.FormValue("command")
	if len(sh) == 0 {
		sh = "sh"
	}
	if !strings.Contains(sh, "sh") {
		internalError(ws, errors.New("command needs to be an sh-like command"))
		return
	}
	if CheckEmpty(podName, namespace, container) {
		internalError(ws, errors.New("[podName, namespace, container] cannot be empty"))
		return
	}

	dLog := log.WithField("podName", podName).WithField("namespace", namespace).WithField("container", container).WithField("command", sh)
	dLog.Info("start ws connect")

	// websocket读写outr/w，exec程序读写inr/w
	outr, outw, err := os.Pipe()
	if err != nil {
		internalError(ws, errors.Wrap(err, "pipe stdout"))
		return
	}
	defer outr.Close()
	defer outw.Close()

	inr, inw, err := os.Pipe()
	if err != nil {
		internalError(ws, errors.Wrap(err, "pipe stdin"))
		return
	}
	defer inr.Close()
	defer inw.Close()

	cmd, err := command.NewCommand(inr, outw, []string{sh}, podName, namespace, container)
	if err != nil {
		internalError(ws, errors.Wrap(err, "NewCommand"))
		return
	}

	resize := make(chan remotecommand.TerminalSize)
	// stream 和 ws 有任意一个中断或结束，需要通知对方停止
	var streamDone bool
	go cmd.Stream(resize, func(err error) {
		streamDone = true
		dLog.Info("stream end, error: ", err)
		if err != nil {
			_ = ws.WriteSafe(websocket.TextMessage, []byte(errors.Wrap(err, "stream error").Error()))
		}
		_ = outr.Close() // 关闭使 pumpStdout 停止
	})

	quit := make(chan struct{})
	go pumpStdout(ws, outr)
	go ping(ws.Conn, quit)

	// ws 连接进行中会在此处堵塞
	pumpStdin(ws.Conn, inw, resize)

	close(quit)
	close(resize)
	_ = ws.Close()
	dLog.Info("end ws connect")

	for i := 0; i < 5; i++ {
		if streamDone {
			return
		}
		// 尝试退出 shell
		_, _ = inw.Write([]byte("exit\n"))
		<-time.After(time.Second)
	}
	if !streamDone {
		dLog.Error("stream did not exit successfully")
	}
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
