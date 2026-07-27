import { Route } from "@solidjs/router";

export const clientRoutes = {
  home: "/",
  settings: "/settings",
};

function Settings() {
  return <div>Settings</div>;
}

function Home() {
  return <div>Home</div>;
}

function App() {
  return (
    <>
      <Route path="/settings" component={Settings} />
      <Route component={Home} path={clientRoutes.home} />
    </>
  );
}
