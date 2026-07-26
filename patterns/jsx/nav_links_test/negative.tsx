// Negative fixture: these hrefs must NOT match any nav_link pattern.
// External URLs and fragments are not part of the app's own route graph.
// These are plain string attributes (no {}), so nav_link_jsx_expr (which
// requires a jsx_expression wrapper) correctly ignores them too.
const Nav = () => (
  <nav>
    <a href="https://example.com">External</a>
    <a href="#top">Top</a>
    <a href="mailto:user@example.com">Email</a>
  </nav>
);
