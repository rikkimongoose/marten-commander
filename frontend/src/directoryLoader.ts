import type { Directory, DirectoryRequest } from "../bindings/github.com/rikkimongoose/marten-commander/internal/desktop/models";

type Call = PromiseLike<Directory> & { cancel?: () => void | PromiseLike<void> };
type Handlers = {
  start: () => void;
  success: (directory: Directory) => void;
  failure: (error: unknown) => void;
};

// One active call and one replaceable pending request per panel. Sequence IDs,
// rather than paths, also protect A -> B -> A and late errors. Cancellation is
// an optimisation; correctness does not depend on the provider honouring it.
export class DirectoryLoader {
  private version = 0;
  private disposed = false;
  private running = false;
  private active: Call | undefined;
  private pending: { request: DirectoryRequest; version: number } | undefined;
  private fetch: (request: DirectoryRequest) => Call;
  private handlers: Handlers;

  constructor(fetch: (request: DirectoryRequest) => Call, handlers: Handlers) {
    this.fetch = fetch;
    this.handlers = handlers;
  }

  request(request: DirectoryRequest) {
    if (this.disposed) return;
    this.pending = { request, version: ++this.version };
    this.handlers.start();
    this.cancelActive();
    void this.run();
  }

  dispose() {
    this.disposed = true;
    this.version++;
    this.pending = undefined;
    this.cancelActive();
  }

  private cancelActive() {
    if (this.active?.cancel) {
      // A failed cancellation must not prevent the next request or become an
      // unhandled rejection. Sequence checks still discard the obsolete result.
      try {
        Promise.resolve(this.active.cancel()).catch(error => console.warn("Cancel directory read:", error));
      } catch (error) {
        console.warn("Cancel directory read:", error);
      }
    }
  }

  private async run() {
    if (this.running || this.disposed) return;
    this.running = true;
    try {
      while (this.pending && !this.disposed) {
        const { request, version } = this.pending;
        this.pending = undefined;
        try {
          this.active = this.fetch(request);
          const directory = await this.active;
          if (!this.disposed && version === this.version) this.handlers.success(directory);
        } catch (error) {
          if (!this.disposed && version === this.version) this.handlers.failure(error);
        } finally {
          this.active = undefined;
        }
      }
    } finally {
      this.running = false;
    }
  }
}
