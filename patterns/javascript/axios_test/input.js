const axios = require('axios');

async function fetchUsers() {
  const res = await axios.get('/api/users');
  return res.data;
}

async function createUser(data) {
  await axios.post('/api/users', data);
}
