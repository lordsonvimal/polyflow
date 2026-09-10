// Structural negatives only (the package gate is not evaluated here): a
// zero-arg call, a two-arg mount, and a bare function call.
app.use();
app.use("/api", router);
useMiddleware(fn);
