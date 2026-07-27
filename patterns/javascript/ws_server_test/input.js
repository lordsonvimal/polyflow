const { WebSocketServer } = require("ws");

const server = require("./http-server");

const wss = new WebSocketServer({ path: "/terminal" });
wss.on("connection", handleConnection);

const wssAttached = new WebSocketServer({ server, verifyClient: (info, cb) => cb(true) });

const wssManual = new WebSocketServer({ noServer: true });

function handleConnection(socket) {
  socket.send("hello");
}
