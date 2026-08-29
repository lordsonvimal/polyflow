const goSocket = new WebSocket("ws://svc-go/notifications");
goSocket.onmessage = (event) => {
  console.log(event.data);
};

const pySocket = new WebSocket("ws://svc-py/updates");
pySocket.onmessage = (event) => {
  console.log(event.data);
};
