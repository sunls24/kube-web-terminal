module kube-web-terminal

go 1.16

require (
	github.com/creack/pty v1.1.18
	github.com/gorilla/websocket v1.5.0
	github.com/mitchellh/go-wordwrap v1.0.1
	github.com/moby/term v0.0.0-20210619224110-3f7ff695adc6
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.8.1
	golang.org/x/sys v0.0.0-20210831042530-f4d43177bf5e
	k8s.io/api v0.23.5
	k8s.io/apimachinery v0.23.5
	k8s.io/client-go v0.23.5
)
