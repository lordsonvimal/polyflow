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
