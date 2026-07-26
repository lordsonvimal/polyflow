// Structural negatives only — the package: express gate is not evaluated by
// the unit fixture matcher, so shapes that would be gate-suppressed at real
// index time (e.g. jQuery's `$.get("/x", cb)`) are deliberately excluded
// here: they'd structurally match this file's queries and this fixture
// would wrongly fail. Only shapes that fail to match structurally belong.
const map = new Map();
map.get("key"); // one arg — not a route registration
app.get("/x"); // no handler arg
fetch("/x", { method: "GET" }); // not a member call
