import "./styles.css";
import App from "./App.svelte";

const target = document.getElementById("app");
if (!target) {
  throw new Error("missing #app mount element");
}

const app = new App({
  target,
});

export default app;
