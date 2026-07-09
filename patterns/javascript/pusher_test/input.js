const pusher = new Pusher(appKey, { cluster: 'mt1' });
const channel = pusher.subscribe('orders');
channel.bind('order:updated', handleOrderUpdate);
