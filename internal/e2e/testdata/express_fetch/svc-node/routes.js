const express = require("express");
const app = express();

app.get("/api/users/:id", getUser);
app.post("/api/users", createUser);

function getUser(req, res) {
  res.json({});
}

function createUser(req, res) {
  res.status(201).json({});
}

app.listen(8080);
