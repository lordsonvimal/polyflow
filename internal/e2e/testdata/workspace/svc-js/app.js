const axios = require('axios');

const API_BASE = '/api';

async function createUser(name, email) {
  const response = await axios.post(`${API_BASE}/users`, { name, email });
  return response.data;
}

async function getUser(id) {
  const response = await axios.get(`${API_BASE}/users/${id}`);
  return response.data;
}

async function deleteUser(id) {
  await axios.delete(`${API_BASE}/users/${id}`);
}

module.exports = { createUser, getUser, deleteUser };
