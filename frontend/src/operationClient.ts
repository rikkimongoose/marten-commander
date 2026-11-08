import type { OperationUpdateEvent, OperationView } from "./operationState.ts";

export interface OperationAPI {
  list(): PromiseLike<readonly OperationView[] | null>;
  cancel(id: string): PromiseLike<OperationView>;
}

export type OperationSubscription = (receive: (event: OperationUpdateEvent) => void) => () => void;

// Events provide low-latency updates; the list call is always made as the
// authoritative recovery path. Version merging protects an event received
// while the list request is in flight from an older response.
export function connectOperationUpdates(
  api: OperationAPI,
  subscribe: OperationSubscription,
  receiveList: (operations: readonly OperationView[]) => void,
  receiveEvent: (event: OperationUpdateEvent) => void,
  failed: (error: unknown) => void,
): () => void {
  let active = true;
  const unsubscribe = subscribe(event => { if (active) receiveEvent(event); });
  void Promise.resolve(api.list()).then(operations => {
    if (active) receiveList(operations ?? []);
  }, error => { if (active) failed(error); });
  return () => { active = false; unsubscribe(); };
}
