
## kube-web-terminal

运行在 `kubernetes` 中，提供 `WEB` 端进入集群内任意 Pod 容器的功能。

### Run

执行 `main.go` 文件，默认监听 `8081` 端口，`-addr` 参数可指定监听地址。

```shell
go run . -addr=":8082"
```

### Build

```shell
docker build -t kube-web-terminal:v0.1 .
```

### Deploy

```shell
kubectl create -f rbac.yaml
kubectl create -f deploy.yaml
```