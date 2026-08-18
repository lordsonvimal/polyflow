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

// wrapper call via identifier (no-substitution template literal URL)
apiFetch(`/api/flows/entrypoints`);

// wrapper obj-style call with a template literal URL
apiFetch({ url: `/api/jobs` });

// WB.2: wrapper obj-style call with a non-"url" key — both keys are now
// captured as candidates; the linker (WB.3) picks one.
apiFetch({ uri: '/a', method: 'GET' });

// WB.4: wrapper call whose URL literal is not the first argument (fleet
// example: _loadVersionHistoryBody(configId, "/dsw/...", loadingClass, ...)).
// producer_alias_url_call no longer anchors to position 0, so this is now
// captured as a candidate; the linker (WB.4) picks it via wrapperURLTable's
// ParamIndex, or index 0 by default if no wrapper fact exists.
loadHistory(configId, '/dsw/app-configs/x/version-history-body');
