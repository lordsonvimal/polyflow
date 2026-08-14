import { createMemo, For } from "solid-js";
import { configStore } from "../../stores/config";

export default function YamlMode() {
  const lines = createMemo(() => configStore.yamlText().split("\n"));

  return (
    <div data-testid="config-yaml-mode" class="flex-1 min-h-0 flex overflow-hidden">
      <div class="select-none text-right pr-2 py-2 text-[11px] text-neutral-500 font-mono bg-neutral-950 overflow-hidden shrink-0">
        <For each={lines()}>{(_, i) => <div>{i() + 1}</div>}</For>
      </div>
      <textarea
        data-testid="config-yaml-textarea"
        class="flex-1 min-h-0 bg-neutral-950 text-neutral-200 font-mono text-[11px] p-2 resize-none outline-none leading-[1.375rem]"
        style={{ "line-height": "1.375rem" }}
        spellcheck={false}
        value={configStore.yamlText()}
        onInput={(e) => configStore.editYamlText(e.currentTarget.value)}
      />
    </div>
  );
}
