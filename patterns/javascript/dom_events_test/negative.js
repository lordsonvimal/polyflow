emitter.on('data', handler);
bus.listen('user:created', callback);
socket.off('message', handler);
