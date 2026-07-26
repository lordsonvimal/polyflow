async function loadUser(id) {
  const resp = await fetch("http://svc-node/api/users/42");
  return resp.json();
}
