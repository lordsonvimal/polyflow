// No path attribute — structurally fails, no match.
const NoPath = () => <Route component={Settings} />;

// No component attribute — structurally fails, no match.
const NoComponent = () => <Route path="/settings" />;

// A locally-defined, unrelated element also named "Route" that happens to
// carry both attributes is structurally indistinguishable from the real
// solid-router shape at the query level — accepted as a known precision
// trade (recall over precision; the package: gate is the real disambiguator
// in a real repo, and fixture tests do not filter by dependency presence).
// Left undeclared here on purpose: this file only asserts the shapes that
// fail structurally.
