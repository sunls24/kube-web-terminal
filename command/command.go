package command

import (
	"fmt"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/homedir"
	"kube-web-terminal/term"
	"os"
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
	Stream(quit <-chan struct{}, resize <-chan remotecommand.TerminalSize) error
}

func NewCommand(ttyRW *os.File, command []string, podName, namespace, container string) (Command, error) {
	if config == nil || client == nil {
		return nil, errors.New("config or client is nil")
	}

	tty := &term.TTY{In: ttyRW, Out: ttyRW}
	tty.Raw = tty.IsTerminalIn()

	req := client.CoreV1().RESTClient().Post().Resource("pods").
		Name(podName).Namespace(namespace).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdin:     true,
			Stderr:    true,
			Stdout:    true,
			TTY:       tty.Raw}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return nil, errors.Wrap(err, "NewSPDYExecutor")
	}
	return &cmd{tty: tty, exec: exec}, nil
}

type cmd struct {
	tty  *term.TTY
	exec remotecommand.Executor
}

func (c *cmd) Stream(quit <-chan struct{}, resize <-chan remotecommand.TerminalSize) error {
	initSize := <-resize
	sizeQueue := c.tty.MonitorSize(resize, &initSize)
	return c.tty.Safe(func() error {
		return c.exec.Stream(remotecommand.StreamOptions{Stdout: c.tty.Out, Stderr: c.tty.Out, Stdin: c.tty.In, Tty: c.tty.Raw, TerminalSizeQueue: sizeQueue})
	}, quit)
}

type Size struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}
