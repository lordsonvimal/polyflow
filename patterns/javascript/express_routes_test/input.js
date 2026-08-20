const express = require("express");
const app = express();
const router = express.Router();
app.get("/api/users/:id", getUser);
router.post("/api/users", (req, res) => { res.send("ok"); });
app.use("/api/v2", router);
app.get(ROUTES.health, healthCheck);
app.use(authMiddleware);
function getUser(req, res) {}
function healthCheck(req, res) {}
