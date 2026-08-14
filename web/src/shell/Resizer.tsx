import { createSignal } from "solid-js";
import { layoutPrefs } from "../stores/layoutPrefs";

// `invert` flips drag direction: the left panel grows as the mouse moves
// right, but a right-docked panel (e.g. DetailHost) grows as the mouse moves
// left — same delta, opposite sign.
export default function Resizer(props?: {
  width?: () => number;
  setWidth?: (w: number) => void;
  invert?: boolean;
}) {
  const [dragging, setDragging] = createSignal(false);
  const getWidth = props?.width ?? layoutPrefs.panelWidth;
  const setWidth = props?.setWidth ?? layoutPrefs.setPanelWidth;
  const sign = props?.invert ? -1 : 1;

  function onMouseDown(e: MouseEvent) {
    e.preventDefault();
    setDragging(true);
    const startX = e.clientX;
    const startWidth = getWidth();

    function onMove(ev: MouseEvent) {
      setWidth(startWidth + sign * (ev.clientX - startX));
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
      data-testid="resizer"
      class={`w-1 cursor-col-resize shrink-0 hover:bg-blue-500 transition-colors ${dragging() ? "bg-blue-500" : "bg-transparent"}`}
      onMouseDown={onMouseDown}
    />
  );
}
