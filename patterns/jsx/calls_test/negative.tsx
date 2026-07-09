// Only PascalCase calls, member calls, and lowercase JSX — none should match.
export function App() {
  Init();
  Utils.format(42);
  return <div className="app"><span /></div>;
}
