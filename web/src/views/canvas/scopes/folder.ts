// One folder's immediate contents: subfolders (collapsed compounds) and
// files, cross-group edges aggregated, boundary edges outside the folder
// (sibling folders/files, other services) rendered as stub connectors.
import { Scope } from "../../../stores/scope";
import { GraphData } from "./common";
import { resolveContainer } from "./container";

export function resolveFolder(scope: Extract<Scope, { kind: "folder" }>, signal?: AbortSignal): Promise<GraphData> {
  return resolveContainer(scope.service, scope.path, signal);
}
