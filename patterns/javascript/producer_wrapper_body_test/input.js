// ── member-access forward, fetch ──
function apiFetchMemberDecl(opts) {
  return fetch(opts.uri);
}
const apiFetchMemberArrow = (opts) => fetch(opts.uri);

// ── member-access forward, axios ──
function apiAxiosMemberDecl(opts) {
  return axios.get(opts.uri);
}
const apiAxiosMemberArrow = (opts) => axios.post(opts.uri);

// ── destructured shorthand, fetch ──
function apiFetchDestructureDecl({ uri }) {
  return fetch(uri);
}
const apiFetchDestructureArrow = ({ uri }) => fetch(uri);

// ── destructured shorthand, axios ──
function apiAxiosDestructureDecl({ uri }) {
  return axios.get(uri);
}
const apiAxiosDestructureArrow = ({ uri }) => axios.get(uri);

// ── destructured + renamed, fetch ──
function apiFetchRenamedDecl({ uri: myUri }) {
  return fetch(myUri);
}
const apiFetchRenamedArrow = ({ uri: myUri }) => fetch(myUri);

// ── destructured + renamed, axios ──
function apiAxiosRenamedDecl({ uri: myUri }) {
  return axios.get(myUri);
}
const apiAxiosRenamedArrow = ({ uri: myUri }) => axios.get(myUri);
