// Literal URL, not forwarded from a parameter — already fetch_call's job.
function apiFetchLiteral(opts) {
  return fetch('/literal');
}

// Member-access on a variable that is NOT the function's own parameter.
function apiFetchLocalVar(opts) {
  const cfg = { uri: '/x' };
  return fetch(cfg.uri);
}

// Destructured key never reaches fetch/axios — plain pass-through.
function apiNotForwarded({ uri }) {
  console.log(uri);
}

// axios call with a hard-coded verb-like property that isn't a real HTTP verb.
function apiAxiosBadVerb(opts) {
  return axios.download(opts.uri);
}

// Unrelated wrapper: forwards to a non-fetch/axios function.
function apiOther(opts) {
  return logRequest(opts.uri);
}

// Object destructured but the local binding used in the call doesn't match
// the source key (mismatched rename).
function apiMismatchedRename({ uri: myUri }) {
  return fetch(otherVar);
}
