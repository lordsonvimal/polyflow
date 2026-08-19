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

statusSocket.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  if (msg.type === 'battery') {
    updateBattery(msg);
  } else if (msg.type === 'charging' && msg.ready) {
    updateCharging(msg);
  }
};

function openTab(tabId) {
  ws?.send({ type: 'create-tab', tabId });
}
