export function Chip(props: { label: string; active: boolean; dashed?: boolean; onClick: () => void }) {
  return (
    <button
      class={`px-2 py-0.5 rounded text-xs border transition-colors ${
        props.active
          ? "bg-neutral-700 text-white border-neutral-600"
          : "bg-transparent text-neutral-400 border-neutral-800 hover:text-neutral-300"
      } ${props.dashed ? "border-dashed" : ""}`}
      onClick={props.onClick}
    >
      {props.label}
    </button>
  );
}
