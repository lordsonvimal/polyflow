const socket = new WebSocket("ws://svc-a/terminal");

socket.onmessage = (event) => {
  console.log(event.data);
};
