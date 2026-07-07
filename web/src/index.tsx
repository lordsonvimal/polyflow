import { render } from "solid-js/web";
import "./index.css";
import App from "./App";

const root = document.getElementById("app");
if (root) {
  render(() => <App />, root);
}
