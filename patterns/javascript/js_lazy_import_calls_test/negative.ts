// Structural negatives only: an arrow with no dynamic import inside, a
// require() (not a dynamic import), and a call with only one argument.
registerHook(() => doSomething(), 'not-an-export');
requireLazy(() => require('./c.js'), 'c');
lonely(() => import('./d.js'));
