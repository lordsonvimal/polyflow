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
