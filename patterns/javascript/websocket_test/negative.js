const client = new EventSource('/api/events');
emitter.on('data', handler);
channel.send(payload);
mailer.send(JSON.stringify({ subject: 'hi' }));
switch (record.kind) {
  case 'a':
    run(record);
    break;
}
if (msg.type !== 'battery') {
  ignore(msg);
}
if (msg.kind === 'battery') {
  ignore(msg);
}
analytics.send({ type: 'pageview', page });
mailer.send({ type: 'welcome', to });
if (node.type === 'leaf') {
  ignore(node);
}
if (props.node.type === 'branch') {
  ignore(props.node);
}
if (e.type === 'keydown') {
  ignore(e);
}
