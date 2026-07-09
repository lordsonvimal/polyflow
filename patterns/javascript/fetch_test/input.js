async function load() {
  const res = await fetch('/api/users');
  const res2 = await fetch('/api/users', { method: 'POST' });
}
