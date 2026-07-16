const Nav = () => (
  <nav>
    <a href="/reports">Reports</a>
    <form action="/reports/export">
      <button type="submit">Export</button>
    </form>
    <form method="post" action="/reports/import">
      <button type="submit">Import</button>
    </form>
  </nav>
);

// G.6 shape (a): ternary href — nav_link_jsx_ternary
const TernaryNav = ({ isAdmin }: { isAdmin: boolean }) => (
  <nav>
    <a href={isAdmin ? "/admin" : "/dashboard"}>Go</a>
  </nav>
);

// G.6 shape (c): dynamic href — nav_link_jsx_dynamic
const DynamicNav = ({ href }: { href: string }) => (
  <nav>
    <a href={href}>Go</a>
  </nav>
);
