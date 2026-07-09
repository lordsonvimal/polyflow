//go:build ignore

package main

func serveWS(w ResponseWriter, r *Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	go readPump(conn)
	go writePump(conn)
}

func readPump(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg Message
		conn.ReadJSON(&msg)
		_ = data
	}
}

func writePump(conn *websocket.Conn) {
	conn.WriteMessage(websocket.TextMessage, payload)
	conn.WriteJSON(response)
}
