// UO.2: Config editor — GET/PUT /api/config (UB.4). Form mode mutates a
// live `yaml` Document (lib/configYaml.ts) so untouched comments survive;
// YAML mode edits raw text directly. Save always PUTs raw text — the
// server never re-marshals (internal/server/config.go's handlePutConfig
// comment: "PUT always takes and writes raw YAML text, never a
// re-marshaled struct").
import { createSignal } from "solid-js";
import type { Document } from "yaml";
import { apiFetch, apiFetchJSON, ApiError } from "../lib/apiFetch";
import { notificationsStore } from "../stores/notifications";
import { jobsStore } from "./jobs";
import {
  parseConfig,
  serialize,
  toModel,
  setField as docSetField,
  pushIn as docPushIn,
  removeAt as docRemoveAt,
  setMapEntry as docSetMapEntry,
  sectionForError,
  countCommentsInSection,
} from "../lib/configYaml";

export type ConfigMode = "form" | "yaml";

export interface ConfigConflict {
  currentEtag: string;
}

export interface ConfigSaveError {
  message: string;
  section: string | null;
}

export interface PendingCommentWarning {
  sections: string[];
}

interface GetConfigResponse {
  path: string;
  raw: string;
  parsed: Record<string, unknown> | null;
  etag: string;
}

const [path, setPath] = createSignal("");
const [rawOnLoad, setRawOnLoad] = createSignal("");
const [etag, setEtag] = createSignal("");
const [doc, setDoc] = createSignal<Document | null>(null, { equals: false });
const [parseError, setParseError] = createSignal<string | null>(null);
const [mode, setModeSignal] = createSignal<ConfigMode>("form");
const [yamlText, setYamlText] = createSignal("");
const [dirty, setDirty] = createSignal(false);
const [loading, setLoading] = createSignal(false);
const [saving, setSaving] = createSignal(false);
const [conflict, setConflict] = createSignal<ConfigConflict | null>(null);
const [saveError, setSaveError] = createSignal<ConfigSaveError | null>(null);
const [diskChanged, setDiskChanged] = createSignal<{ etag: string } | null>(null);
const [pendingWarning, setPendingWarning] = createSignal<PendingCommentWarning | null>(null);

// Sections touched by form-mode edits this session — narrows the pre-save
// comment-loss check to what actually changed instead of scanning the
// whole file.
const touchedSections = new Set<string>();

function parseErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    try {
      const body = JSON.parse(err.body) as { error?: string };
      return body.error || err.body || err.message;
    } catch {
      return err.body || err.message;
    }
  }
  return err instanceof Error ? err.message : String(err);
}

function applyLoaded(resp: GetConfigResponse): void {
  setPath(resp.path);
  setRawOnLoad(resp.raw);
  setEtag(resp.etag);
  const { doc: parsedDoc, error } = parseConfig(resp.raw);
  setDoc(parsedDoc);
  setParseError(error);
  setYamlText(resp.raw);
  setDirty(false);
  touchedSections.clear();
}

async function load(): Promise<void> {
  setLoading(true);
  try {
    const resp = await apiFetchJSON<GetConfigResponse>("/api/config", { silent: true });
    applyLoaded(resp);
  } catch (err) {
    notificationsStore.add({
      id: `config-load-err-${Date.now()}`,
      kind: "error",
      message: "Failed to load config",
      detail: parseErrorMessage(err),
    });
  } finally {
    setLoading(false);
  }
}

// UO.2 spec: refetch on window focus; etag drift shows a banner rather
// than silently overwriting local edits.
async function checkDiskChange(): Promise<void> {
  if (!path()) return;
  try {
    const resp = await apiFetchJSON<GetConfigResponse>("/api/config", { silent: true });
    if (resp.etag !== etag()) setDiskChanged({ etag: resp.etag });
  } catch {
    // Transient — the next focus event (or an explicit save) will surface it.
  }
}

function setMode(next: ConfigMode): void {
  if (next === mode()) return;
  if (next === "yaml") {
    const d = doc();
    if (d) setYamlText(serialize(d));
    setModeSignal("yaml");
    return;
  }
  // yaml -> form: re-parse the edited text; block the switch on a parse
  // error rather than silently discarding it.
  const { doc: parsedDoc, error } = parseConfig(yamlText());
  if (error) {
    setParseError(error);
    return;
  }
  setDoc(parsedDoc);
  setParseError(null);
  setModeSignal("form");
}

function currentRaw(): string {
  if (mode() === "yaml") return yamlText();
  const d = doc();
  return d ? serialize(d) : yamlText();
}

function editYamlText(text: string): void {
  setYamlText(text);
  setDirty(true);
}

function withDoc(section: string, mutate: (d: Document) => void): void {
  const d = doc();
  if (!d) return;
  mutate(d);
  touchedSections.add(section);
  setDirty(true);
  setDoc(d);
}

function setField(path_: (string | number)[], value: unknown, section = String(path_[0])): void {
  withDoc(section, (d) => docSetField(d, path_, value));
}

function addRow(path_: (string | number)[], value: unknown, section = String(path_[0])): void {
  withDoc(section, (d) => docPushIn(d, path_, value));
}

function removeRow(path_: (string | number)[], index: number, section = String(path_[0])): void {
  withDoc(section, (d) => docRemoveAt(d, path_, index));
}

function setMapEntry(
  path_: (string | number)[],
  key: string,
  value: string | undefined,
  section = String(path_[0]),
): void {
  withDoc(section, (d) => docSetMapEntry(d, path_, key, value));
}

function model(): Record<string, any> {
  const d = doc();
  return d ? toModel(d) : {};
}

function clearSaveState(): void {
  setSaveError(null);
  setConflict(null);
  setPendingWarning(null);
}

async function doSave(raw: string): Promise<void> {
  setSaving(true);
  try {
    const res = await apiFetch("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ raw, etag: etag() }),
      silent: true,
    });
    const data = (await res.json()) as { etag: string; ok: boolean };
    setRawOnLoad(raw);
    setEtag(data.etag);
    setDirty(false);
    touchedSections.clear();
    clearSaveState();
    notificationsStore.add({
      id: `config-save-${Date.now()}`,
      kind: "success",
      message: "Config saved",
      action: { label: "Re-index now?", onClick: () => jobsStore.startIndex(false) },
    });
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      try {
        const body = JSON.parse(err.body) as { current_etag?: string };
        setConflict({ currentEtag: body.current_etag ?? "" });
      } catch {
        setConflict({ currentEtag: "" });
      }
      return;
    }
    if (err instanceof ApiError && err.status === 422) {
      setSaveError({ message: err.body, section: sectionForError(raw, err.body) });
      return;
    }
    notificationsStore.add({
      id: `config-save-err-${Date.now()}`,
      kind: "error",
      message: "Failed to save config",
      detail: parseErrorMessage(err),
    });
  } finally {
    setSaving(false);
  }
}

// force=true skips the comment-loss confirmation (called after the user
// confirms the warning dialog).
async function save(force = false): Promise<void> {
  clearSaveState();
  const raw = currentRaw();
  if (mode() === "form" && !force) {
    const lost = [...touchedSections].filter((s) => countCommentsInSection(raw, s) < countCommentsInSection(rawOnLoad(), s));
    if (lost.length > 0) {
      setPendingWarning({ sections: lost });
      return;
    }
  }
  await doSave(raw);
}

function cancelWarning(): void {
  setPendingWarning(null);
}

async function confirmWarningAndSave(): Promise<void> {
  setPendingWarning(null);
  await doSave(currentRaw());
}

// 409 "keep mine": adopt the disk's current etag and re-attempt the write
// with local content — an explicit overwrite, not a silent one.
async function keepMine(): Promise<void> {
  const c = conflict();
  if (!c) return;
  setEtag(c.currentEtag);
  setConflict(null);
  await save(true);
}

// 409 "take disk": discard local edits, reload from server.
async function takeDisk(): Promise<void> {
  setConflict(null);
  await load();
}

function cancelConflict(): void {
  setConflict(null);
}

function reloadFromDiskBanner(): void {
  setDiskChanged(null);
  load();
}

function dismissDiskChanged(): void {
  setDiskChanged(null);
}

if (typeof window !== "undefined") {
  window.addEventListener("focus", () => {
    checkDiskChange();
  });
}

export const configStore = {
  path,
  rawOnLoad,
  etag,
  doc,
  model,
  parseError,
  mode,
  yamlText,
  dirty,
  loading,
  saving,
  conflict,
  saveError,
  diskChanged,
  pendingWarning,
  load,
  setMode,
  editYamlText,
  setField,
  addRow,
  removeRow,
  setMapEntry,
  save,
  cancelWarning,
  confirmWarningAndSave,
  keepMine,
  takeDisk,
  cancelConflict,
  reloadFromDiskBanner,
  dismissDiskChanged,
  checkDiskChange,
  // Test-only: singleton store reset between cases.
  reset: () => {
    setPath("");
    setRawOnLoad("");
    setEtag("");
    setDoc(null);
    setParseError(null);
    setModeSignal("form");
    setYamlText("");
    setDirty(false);
    setLoading(false);
    setSaving(false);
    setConflict(null);
    setSaveError(null);
    setDiskChanged(null);
    setPendingWarning(null);
    touchedSections.clear();
  },
};
