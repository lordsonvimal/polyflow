async function loadData() {
    const res = await fetch('/api/data');
    const res2 = await fetch('/api/items', { method: 'POST' });
}
