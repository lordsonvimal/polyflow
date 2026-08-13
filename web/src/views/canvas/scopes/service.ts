// One service's internals: top-level folders (and loose top-level files) as
// collapsed compounds, cross-folder edges aggregated, boundary edges to
// other services rendered as stub connectors.
import { Scope } from "../../../stores/scope";
import { GraphData } from "./common";
import { resolveContainer } from "./container";

export function resolveService(scope: Extract<Scope, { kind: "service" }>, signal?: AbortSignal): Promise<GraphData> {
  return resolveContainer(scope.service, "", signal);
}
