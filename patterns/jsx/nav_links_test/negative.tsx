// Negative fixture: these hrefs must NOT match any nav_link pattern.
// External URLs and fragments are not part of the app's own route graph.
const Nav = () => (
  <nav>
    <a href="https://example.com">External</a>
    <a href="#top">Top</a>
    <a href="mailto:user@example.com">Email</a>
  </nav>
);

// Non-string-literal nested ternary (branches are not string literals at depth 1)
// nav_link_jsx_ternary must NOT match this.
const NestedNav = ({ isAdmin, isGuest }: { isAdmin: boolean; isGuest: boolean }) => (
  <nav>
    <a href={isAdmin ? (isGuest ? "/guest" : "/admin") : "/home"}>Go</a>
  </nav>
);
