const client = new EventSource('/api/events');
emitter.on('data', handler);
channel.send(payload);
mailer.send(JSON.stringify({ subject: 'hi' }));
switch (record.kind) {
  case 'a':
    run(record);
    break;
}
