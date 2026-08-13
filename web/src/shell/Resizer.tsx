import { createSignal } from "solid-js";
import { layoutPrefs } from "../stores/layoutPrefs";

export default function Resizer() {
  const [dragging, setDragging] = createSignal(false);

  function onMouseDown(e: MouseEvent) {
    e.preventDefault();
    setDragging(true);
    const startX = e.clientX;
    const startWidth = layoutPrefs.panelWidth();

    function onMove(ev: MouseEvent) {
      layoutPrefs.setPanelWidth(startWidth + ev.clientX - startX);
    }
    function onUp() {
      setDragging(false);
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
    }
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  }

  return (
    <div
      class={`w-1 cursor-col-resize shrink-0 hover:bg-blue-500 transition-colors ${dragging() ? "bg-blue-500" : "bg-transparent"}`}
      onMouseDown={onMouseDown}
    />
  );
}
