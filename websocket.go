package main

import (
	"github.com/gorilla/websocket"
	"net/http"
	"sync"
)

type Conn struct {
	*websocket.Conn
	m sync.Mutex
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	conn, err := upgrader.Upgrade(w, r, nil)
	return &Conn{Conn: conn}, err
}

func (c *Conn) WriteSafe(messageType int, data []byte) error {
	c.m.Lock()
	defer c.m.Unlock()
	return c.WriteMessage(messageType, data)
}
