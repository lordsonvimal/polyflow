package main

import (
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{}

func serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	go readPump(conn)
}

// readPump reads typed JSON messages and dispatches by msg.Type.
func readPump(conn *websocket.Conn) {
	for {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case "battery":
			recordBattery(msg)
			conn.WriteJSON(Ack{Type: "battery_ack", Status: "ok"})
		}
	}
}

func recordBattery(msg Message) {
	_ = msg
}
