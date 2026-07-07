import axios from 'axios';

async function loadUsers() {
    const res = await axios.get('/api/users');
    return res.data;
}

async function createUser(data) {
    return axios.post('/api/users', data);
}
