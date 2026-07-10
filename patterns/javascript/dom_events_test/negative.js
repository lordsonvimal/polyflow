emitter.on('data', handler);
bus.listen('user:created', callback);
socket.off('message', handler);
ws.onmessage = (evt) => dispatch(evt); // WebSocket/SSE pattern, not DOM
obj.onX = fn; // uppercase after "on" — not a DOM event property
