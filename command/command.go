package command

import (
	"fmt"
	"github.com/pkg/errors"
	"io"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/homedir"
	"kube-web-terminal/term"
)

var config *rest.Config
var client *kubernetes.Clientset

func init() {
	// 初始化集群配置文件
	var err error
	config, err = rest.InClusterConfig()
	if err != nil {
		if errors.Is(err, rest.ErrNotInCluster) {
			home := homedir.HomeDir()
			config, err = clientcmd.BuildConfigFromFlags("", fmt.Sprintf("%s/.kube/config", home))
			if err != nil {
				panic(errors.Wrap(err, "build kube config in $HOME"))
			}
		} else {
			panic(errors.Wrap(err, "rest.InClusterConfig"))
		}
	}

	if config == nil {
		return
	}
	client, err = kubernetes.NewForConfig(config)
	if err != nil {
		panic(errors.Wrap(err, "kubernetes.NewForConfig"))
	}
}

type Command interface {
	Stream(resize <-chan remotecommand.TerminalSize, errHandle func(error))
}

func NewCommand(in io.Reader, out io.Writer, command []string, podName, namespace, container string) (Command, error) {
	if config == nil || client == nil {
		return nil, errors.New("config or client is nil")
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
	return &cmd{in: in, out: out, exec: exec}, nil
}

type cmd struct {
	in   io.Reader
	out  io.Writer
	exec remotecommand.Executor
}

func (c *cmd) Stream(resize <-chan remotecommand.TerminalSize, errHandler func(error)) {
	initSize, ok := <-resize
	if !ok {
		errHandler(errors.New("resize is closed, stream not start"))
		return
	}
	sizeQueue := term.MonitorSize(resize, initSize)
	errHandler(c.exec.Stream(remotecommand.StreamOptions{Stdout: c.out, Stderr: c.out, Stdin: c.in, Tty: true, TerminalSizeQueue: sizeQueue}))
}

type Size struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}
