// ── positional forward (WB.4), fetch ──
function apiFetchPositionalDecl(configId, uri) {
  fetch(uri).then(function () {});
}

// ── positional forward (WB.4), axios ──
function apiAxiosPositionalDecl(configId, uri) {
  axios.get(uri).then(function () {});
}

// ── axios(config-object) forward, pair form ──
function apiPutPairDecl(configId, uri) {
  return axios({ method: "PUT", url: uri });
}

// ── axios(config-object) forward, shorthand form ──
const apiGetShorthandArrow = (url) => axios({ method: "GET", url });

// ── UB.1: object-literal transport, positional forward ──
const NetUtils = {
  getJson: (uri) => fetch(uri),
  postForm(uri, body) {
    return fetch(uri, { method: "POST", body });
  },
};

// ── UB.1: single unparenthesised arrow parameter ──
const apiGetBareArrow = uri => fetch(uri);
