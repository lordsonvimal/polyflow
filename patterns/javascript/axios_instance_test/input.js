const axios = require('axios');

// axios instance with baseURL
const api = axios.create({ baseURL: '/api' });

// calls through the instance
api.get('/users');
api.post('/orders', payload);
