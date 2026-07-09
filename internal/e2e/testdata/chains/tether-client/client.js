const ws = new WebSocket('wss://tether.local/socket');

function reportBattery(level) {
  ws.send(JSON.stringify({ type: 'battery', level }));
}

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  switch (msg.type) {
    case 'battery_ack':
      showAck(msg);
      break;
  }
};

function showAck(msg) {
  document.getElementById('ack').textContent = msg.status;
}
