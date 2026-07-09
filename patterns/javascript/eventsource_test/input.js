const es = new EventSource('/api/events');
es.onmessage = (evt) => {
  console.log(evt.data);
};
