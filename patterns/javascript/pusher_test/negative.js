// X.1a: pusher_subscribe_client's channel capture is loosened from (string)
// to (_) (a variable-held channel name is a legitimate producer key), so a
// single-arg subscribe(<identifier>) is no longer structurally distinguishable
// from an EventEmitter-style subscribe(callback) — that precision now comes
// from the package: pusher-js gate (services without the dependency never
// load this pattern), not from this query. This fixture harness runs
// ungated, so it only proves the arity/shape guards that remain query-level.
fn.bind(this, arg);
store.subscribe('topic', callback);
