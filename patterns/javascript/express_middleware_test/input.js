const express = require("express");
const app = express();
app.use(authMiddleware);
app.use(cors());
app.get("/x", handler);
function authMiddleware(req, res, next) { next(); }
function handler(req, res) {}
