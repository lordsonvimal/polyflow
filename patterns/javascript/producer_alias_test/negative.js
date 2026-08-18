// Member expression calls — not a standalone identifier call
axios.get('/users');
$.ajax('/path');

// Not a fetch or $.ajax binding
const img = Image;
const q = query;

// URL is not a string literal (variable reference)
f(dynamicUrl);
a(computedPath);

// WB.2 non-goal: spread + shorthand prop — not a `pair` node, no key/value to capture.
a({ ...opts, url });

// WB.2 non-goal: computed key — never matched, not a regression.
a({ [k]: '/x' });
