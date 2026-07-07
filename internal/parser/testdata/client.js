import axios from 'axios';

async function fetchUser(id) {
  const res = await axios.get(`/api/users/${id}`);
  return res.data;
}

async function createPost(data) {
  return axios.post('/api/posts', data);
}

fetch('/api/health').then(r => r.json());
