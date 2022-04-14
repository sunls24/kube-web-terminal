FROM golang:1.17.9-alpine3.15 as builder
COPY . /opt/src
WORKDIR /opt/src
RUN CGO_ENABLED=0 GOOS=linux GOPROXY=https://goproxy.cn,direct go build -o /opt/kube-web-terminal .

FROM alpine:3.15.4
COPY --from=builder /opt/kube-web-terminal /opt/
CMD ["/opt/kube-web-terminal"]
