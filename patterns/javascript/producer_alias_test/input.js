// fetch alias binding: const f = fetch
const f = fetch;

// jquery alias binding: const a = $.ajax
const a = $.ajax;

// axios member destructure: const get = axios.get
const get = axios.get;

// axios shorthand destructure: const { post } = axios
const { post } = axios;

// alias call via identifier (string URL)
f('/users');
a('/orders');

// alias obj-style call
a({ url: '/items' });
