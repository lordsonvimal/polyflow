import { For, createMemo } from "solid-js";
import { configStore } from "../../stores/config";

interface Service {
  name?: string;
  path?: string;
  language?: string;
  frameworks?: string[];
  port?: number;
}

interface Link {
  from?: string;
  to?: string;
  via?: string;
  hint?: string;
  base_url?: string;
  exchange?: string;
}

function Field(props: { label: string; testid: string; value: string; onInput: (v: string) => void; placeholder?: string }) {
  return (
    <label class="flex flex-col gap-0.5 text-[11px] text-neutral-400">
      {props.label}
      <input
        data-testid={props.testid}
        class="bg-neutral-900 border border-neutral-800 rounded px-1.5 py-1 text-neutral-200 text-xs"
        value={props.value}
        placeholder={props.placeholder}
        onInput={(e) => props.onInput(e.currentTarget.value)}
      />
    </label>
  );
}

function Section(props: { title: string; children: any }) {
  return (
    <div class="space-y-2">
      <h3 class="text-xs font-semibold text-neutral-300 uppercase tracking-wide">{props.title}</h3>
      {props.children}
    </div>
  );
}

function ServicesSection(props: { services: Service[] }) {
  return (
    <Section title="Services">
      <For each={props.services}>
        {(svc, i) => (
          <div data-testid="config-service-row" class="border border-neutral-800 rounded p-2 grid grid-cols-2 gap-2 mb-2">
            <Field
              label="Name"
              testid={`config-service-name-${i()}`}
              value={svc.name ?? ""}
              onInput={(v) => configStore.setField(["services", i(), "name"], v)}
            />
            <Field
              label="Path"
              testid={`config-service-path-${i()}`}
              value={svc.path ?? ""}
              onInput={(v) => configStore.setField(["services", i(), "path"], v)}
            />
            <Field
              label="Language"
              testid={`config-service-language-${i()}`}
              value={svc.language ?? ""}
              onInput={(v) => configStore.setField(["services", i(), "language"], v)}
            />
            <Field
              label="Port"
              testid={`config-service-port-${i()}`}
              value={svc.port !== undefined ? String(svc.port) : ""}
              onInput={(v) => configStore.setField(["services", i(), "port"], v === "" ? undefined : Number(v))}
            />
            <div class="col-span-2">
              <Field
                label="Frameworks (comma-separated)"
                testid={`config-service-frameworks-${i()}`}
                value={(svc.frameworks ?? []).join(", ")}
                onInput={(v) =>
                  configStore.setField(
                    ["services", i(), "frameworks"],
                    v.split(",").map((s) => s.trim()).filter(Boolean),
                  )
                }
              />
            </div>
            <button
              data-testid={`config-remove-service-${i()}`}
              class="col-span-2 text-left text-red-400 hover:text-red-300 text-[11px]"
              onClick={() => configStore.removeRow(["services"], i())}
            >
              Remove service
            </button>
          </div>
        )}
      </For>
      <button
        data-testid="config-add-service"
        class="text-indigo-300 hover:text-indigo-200 text-xs"
        onClick={() => configStore.addRow(["services"], { name: "", path: "", language: "" })}
      >
        + Add service
      </button>
    </Section>
  );
}

function LinksSection(props: { links: Link[] }) {
  return (
    <Section title="Links">
      <For each={props.links}>
        {(link, i) => (
          <div data-testid="config-link-row" class="border border-neutral-800 rounded p-2 grid grid-cols-2 gap-2 mb-2">
            <Field label="From" testid={`config-link-from-${i()}`} value={link.from ?? ""} onInput={(v) => configStore.setField(["links", i(), "from"], v)} />
            <Field label="To" testid={`config-link-to-${i()}`} value={link.to ?? ""} onInput={(v) => configStore.setField(["links", i(), "to"], v)} />
            <Field label="Via" testid={`config-link-via-${i()}`} value={link.via ?? ""} onInput={(v) => configStore.setField(["links", i(), "via"], v)} />
            <Field label="Hint" testid={`config-link-hint-${i()}`} value={link.hint ?? ""} onInput={(v) => configStore.setField(["links", i(), "hint"], v)} />
            <Field label="Base URL" testid={`config-link-baseurl-${i()}`} value={link.base_url ?? ""} onInput={(v) => configStore.setField(["links", i(), "base_url"], v)} />
            <Field label="Exchange" testid={`config-link-exchange-${i()}`} value={link.exchange ?? ""} onInput={(v) => configStore.setField(["links", i(), "exchange"], v)} />
            <button
              data-testid={`config-remove-link-${i()}`}
              class="col-span-2 text-left text-red-400 hover:text-red-300 text-[11px]"
              onClick={() => configStore.removeRow(["links"], i())}
            >
              Remove link
            </button>
          </div>
        )}
      </For>
      <button
        data-testid="config-add-link"
        class="text-indigo-300 hover:text-indigo-200 text-xs"
        onClick={() => configStore.addRow(["links"], { from: "", to: "" })}
      >
        + Add link
      </button>
    </Section>
  );
}

function ExcludesSection(props: { excludes: string[] }) {
  return (
    <Section title="Excludes">
      <For each={props.excludes}>
        {(glob, i) => (
          <div data-testid="config-exclude-row" class="flex items-center gap-2 mb-1">
            <input
              data-testid={`config-exclude-${i()}`}
              class="flex-1 bg-neutral-900 border border-neutral-800 rounded px-1.5 py-1 text-neutral-200 text-xs"
              value={glob}
              onInput={(e) => configStore.setField(["index", "exclude", i()], e.currentTarget.value)}
            />
            <button
              data-testid={`config-remove-exclude-${i()}`}
              class="text-red-400 hover:text-red-300 text-[11px]"
              onClick={() => configStore.removeRow(["index", "exclude"], i())}
            >
              ✕
            </button>
          </div>
        )}
      </For>
      <button
        data-testid="config-add-exclude"
        class="text-indigo-300 hover:text-indigo-200 text-xs"
        onClick={() => configStore.addRow(["index", "exclude"], "")}
      >
        + Add exclude glob
      </button>
    </Section>
  );
}

function SettingsSection(props: { settings: Record<string, any> }) {
  return (
    <Section title="Settings">
      <div class="grid grid-cols-2 gap-2">
        <Field
          label="Snippet lines"
          testid="config-settings-snippet-lines"
          value={props.settings.snippet_lines !== undefined ? String(props.settings.snippet_lines) : ""}
          onInput={(v) => configStore.setField(["settings", "snippet_lines"], v === "" ? undefined : Number(v))}
        />
        <Field
          label="Default layout"
          testid="config-settings-default-layout"
          value={props.settings.default_layout ?? ""}
          onInput={(v) => configStore.setField(["settings", "default_layout"], v)}
        />
        <Field
          label="Default depth"
          testid="config-settings-default-depth"
          value={props.settings.default_depth !== undefined ? String(props.settings.default_depth) : ""}
          onInput={(v) => configStore.setField(["settings", "default_depth"], v === "" ? undefined : Number(v))}
        />
        <Field
          label="Port"
          testid="config-settings-port"
          value={props.settings.port !== undefined ? String(props.settings.port) : ""}
          onInput={(v) => configStore.setField(["settings", "port"], v === "" ? undefined : Number(v))}
        />
      </div>
    </Section>
  );
}

function EvidenceSection(props: { evidence: Record<string, any> }) {
  const serviceNames = () => (props.evidence.runtime?.service_names ?? {}) as Record<string, string>;
  const sseRoutes = () => (props.evidence.runtime?.sse_routes ?? []) as string[];
  const contractGlobs = () => (props.evidence.contract_globs ?? []) as string[];

  return (
    <Section title="Evidence">
      <div class="space-y-1">
        <div class="text-[11px] text-neutral-400">Contract globs</div>
        <For each={contractGlobs()}>
          {(glob, i) => (
            <div data-testid="config-contract-glob-row" class="flex items-center gap-2 mb-1">
              <input
                data-testid={`config-contract-glob-${i()}`}
                class="flex-1 bg-neutral-900 border border-neutral-800 rounded px-1.5 py-1 text-neutral-200 text-xs"
                value={glob}
                onInput={(e) => configStore.setField(["evidence", "contract_globs", i()], e.currentTarget.value)}
              />
              <button
                data-testid={`config-remove-contract-glob-${i()}`}
                class="text-red-400 hover:text-red-300 text-[11px]"
                onClick={() => configStore.removeRow(["evidence", "contract_globs"], i())}
              >
                ✕
              </button>
            </div>
          )}
        </For>
        <button
          data-testid="config-add-contract-glob"
          class="text-indigo-300 hover:text-indigo-200 text-xs"
          onClick={() => configStore.addRow(["evidence", "contract_globs"], "")}
        >
          + Add contract glob
        </button>
      </div>

      <div class="space-y-1">
        <div class="text-[11px] text-neutral-400">Runtime service names (otel name → workspace service)</div>
        <For each={Object.entries(serviceNames())}>
          {([otelName, svcName]) => (
            <div data-testid="config-service-name-row" class="flex items-center gap-2 mb-1">
              <span class="text-xs text-neutral-400 w-32 truncate" title={otelName}>{otelName}</span>
              <input
                data-testid={`config-service-name-value-${otelName}`}
                class="flex-1 bg-neutral-900 border border-neutral-800 rounded px-1.5 py-1 text-neutral-200 text-xs"
                value={svcName}
                onInput={(e) => configStore.setMapEntry(["evidence", "runtime", "service_names"], otelName, e.currentTarget.value, "evidence")}
              />
              <button
                data-testid={`config-remove-service-name-${otelName}`}
                class="text-red-400 hover:text-red-300 text-[11px]"
                onClick={() => configStore.setMapEntry(["evidence", "runtime", "service_names"], otelName, undefined, "evidence")}
              >
                ✕
              </button>
            </div>
          )}
        </For>
      </div>

      <div class="space-y-1">
        <div class="text-[11px] text-neutral-400">SSE routes</div>
        <For each={sseRoutes()}>
          {(route, i) => (
            <div data-testid="config-sse-route-row" class="flex items-center gap-2 mb-1">
              <input
                data-testid={`config-sse-route-${i()}`}
                class="flex-1 bg-neutral-900 border border-neutral-800 rounded px-1.5 py-1 text-neutral-200 text-xs"
                value={route}
                onInput={(e) => configStore.setField(["evidence", "runtime", "sse_routes", i()], e.currentTarget.value, "evidence")}
              />
              <button
                data-testid={`config-remove-sse-route-${i()}`}
                class="text-red-400 hover:text-red-300 text-[11px]"
                onClick={() => configStore.removeRow(["evidence", "runtime", "sse_routes"], i(), "evidence")}
              >
                ✕
              </button>
            </div>
          )}
        </For>
        <button
          data-testid="config-add-sse-route"
          class="text-indigo-300 hover:text-indigo-200 text-xs"
          onClick={() => configStore.addRow(["evidence", "runtime", "sse_routes"], "", "evidence")}
        >
          + Add SSE route
        </button>
      </div>
    </Section>
  );
}

export default function FormMode() {
  const model = createMemo(() => configStore.model());

  return (
    <div data-testid="config-form-mode" class="p-3 space-y-4 overflow-y-auto">
      <ServicesSection services={model().services ?? []} />
      <LinksSection links={model().links ?? []} />
      <ExcludesSection excludes={model().index?.exclude ?? []} />
      <SettingsSection settings={model().settings ?? {}} />
      <EvidenceSection evidence={model().evidence ?? {}} />
    </div>
  );
}
