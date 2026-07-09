const ws = new WebSocket('wss://tether.local/socket');

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  switch (msg.type) {
    case 'battery':
      updateBattery(msg);
      break;
    case 'location':
      updateLocation(msg);
      break;
  }
};

function reportBattery(level) {
  ws.send(JSON.stringify({ type: 'battery', level }));
}

server.on('connection', (socket) => {
  socket.on('message', handleMessage);
});
