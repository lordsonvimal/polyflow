// Structural negatives only. `.on("connection", h)` is gated by package: ws
// (not anchored to a same-file ws_server_new capture — tree-sitter cannot
// express that), so a same-shape emitter with a different event name/arity
// is what's actually testable here.
emitter.on("message", handleMessage); // wrong event name
emitter.on("connection"); // no handler arg
new WebSocketServer(); // no config object at all
new SomeOtherServer({ path: "/x" }); // wrong constructor name
