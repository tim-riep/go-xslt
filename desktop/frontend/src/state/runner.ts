// The run loop every *RunView shares: flush pending file edits to disk (the
// engine resolves xsl:include/xs:import/document() from disk, so a run must
// never see a stale on-disk copy of a file someone is actively editing),
// then execute, recording the outcome in the results store under this tab's
// id. `execute` is supplied by the caller (already bound to the run's
// current stylesheet/source/params/etc.) and memoized with useCallback so a
// stable `run` identity survives re-renders unless its own inputs changed.
import { useCallback } from "react";
import { buffers } from "./buffers";
import { results, useResult, type RunResultEntry } from "./results";

export function useRunner<TResult>(tabId: string, projectId: string, execute: () => Promise<TResult>): RunResultEntry<TResult> & { run: () => Promise<void> } {
  const entry = useResult<TResult>(tabId);

  const run = useCallback(async () => {
    const seq = results.start(tabId);
    const t0 = performance.now();
    try {
      await buffers.flushProject(projectId);
      const result = await execute();
      results.finish(tabId, seq, result, Math.round(performance.now() - t0));
    } catch (e) {
      results.fail(tabId, seq, e instanceof Error ? e.message : String(e));
    }
  }, [tabId, projectId, execute]);

  return { ...entry, run };
}
