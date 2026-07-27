const { WebSocketServer } = require("ws");

const wss = new WebSocketServer({ path: "/terminal" });

wss.on("connection", handleConnection);

function handleConnection(socket) {
  socket.send("hello");
}
