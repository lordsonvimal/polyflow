import { Component } from "solid-js";

const Layout: Component = () => {
  return <div>layout</div>;
};

const App: Component = () => {
  return (
    <div>
      <Layout>
        <Graph />
      </Layout>
    </div>
  );
};

export default App;
