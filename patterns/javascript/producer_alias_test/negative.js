// Member expression calls — not a standalone identifier call
axios.get('/users');
$.ajax('/path');

// Not a fetch or $.ajax binding
const img = Image;
const q = query;

// URL is not a string literal (variable reference)
f(dynamicUrl);
a(computedPath);

// Object call with non-url key
a({ method: 'GET' });
