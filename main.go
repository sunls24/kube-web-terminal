package main

import (
	"bufio"
	"flag"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"io"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"net/http"
	"os"
	"strings"
	"time"
)

func kubeCommand(in io.Reader, out io.Writer, command []string, podName, namespace, container string) (func() error, error) {
	config, err := clientcmd.BuildConfigFromFlags("", "/home/sunls/.kube/config")
	if err != nil {
		return nil, errors.Wrap(err, "build kube config")
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, errors.Wrap(err, "new client by kube config")
	}
	req := client.CoreV1().RESTClient().Post().Resource("pods").
		Name(podName).Namespace(namespace).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     true,
			Stderr:    true,
			Stdout:    true,
			TTY:       true}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return nil, errors.Wrap(err, "NewSPDYExecutor")
	}

	return func() error {
		return exec.Stream(remotecommand.StreamOptions{Stdout: out, Stderr: out, Stdin: in, Tty: true})
	}, nil
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

func pumpStdin(ws *websocket.Conn, w io.Writer) {
	ws.SetReadLimit(maxMessageSize)
	_ = ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error { _ = ws.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			break
		}

		message = append(message, '\n')
		if _, err = w.Write(message); err != nil {
			break
		}
	}
}

func pumpStdout(ws *websocket.Conn, r io.Reader, done chan struct{}) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
		if err := ws.WriteMessage(websocket.TextMessage, s.Bytes()); err != nil {
			break
		}
	}

	_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
	_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	close(done)
}

func ping(ws *websocket.Conn, done chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
				log.Error(errors.Wrap(err, "ping"))
			}
		case <-done:
			return
		}
	}
}

func internalError(ws *websocket.Conn, err error) {
	err = errors.Wrap(err, "internal server error")
	log.Error(err)
	_ = ws.WriteMessage(websocket.TextMessage, []byte(err.Error()))
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
	return true
}}

func serveWs(w http.ResponseWriter, r *http.Request) {
	podName := r.FormValue("podName")
	namespace := r.FormValue("namespace")
	container := r.FormValue("container")
	command := r.FormValue("command")
	if len(command) == 0 {
		command = "sh"
	}
	if !strings.Contains(command, "sh") {
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

	dLog := log.WithField("podName", podName).WithField("namespace", namespace).WithField("container", container).WithField("command", command)
	dLog.Info("start ws connect")

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

	stream, err := kubeCommand(inr, outw, []string{command}, podName, namespace, container)
	if err != nil {
		internalError(ws, errors.Wrap(err, "kube command"))
		return
	}

	// stream 和 ws 有任意一个中断或结束，需要通知对方停止
	var streamDone bool
	go func() {
		dLog.Info("stream start")
		err := stream()
		dLog.Info("stream end, error: ", err)
		streamDone = true
		_ = outr.Close() // 关闭使 pumpStdout 停止
	}()

	stdoutDone := make(chan struct{})
	go pumpStdout(ws, outr, stdoutDone)
	go ping(ws, stdoutDone)

	// ws 连接进行中会在此处堵塞
	pumpStdin(ws, inw)

	for i := 0; i < 5; i++ {
		if streamDone {
			break
		}
		_, _ = inw.Write([]byte("exit\n"))
		<-time.After(time.Second)
	}
	if !streamDone {
		dLog.Error("stream did not exit successfully")
	}

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

func init() {
	format := new(log.TextFormatter)
	format.FullTimestamp = true
	format.TimestampFormat = "06-01-02 15:04:05"
	log.SetFormatter(format)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8081", "http service address")
	flag.Parse()

	http.HandleFunc("/", serveHome)
	http.HandleFunc("/ws", serveWs)
	log.Info("start http listen addr: ", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
