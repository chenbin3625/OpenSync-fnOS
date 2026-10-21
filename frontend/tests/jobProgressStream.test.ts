import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  applyProgressFrame,
  createJobProgressStream,
  jobProgressStreamUrl,
  parseProgressEnvelope,
  type JobProgressStreamState,
} from "../src/pages/Home/jobProgressStream";
import type { CurrentTaskData } from "../src/types";

type MessageHandler = ((event: { data: string }) => void) | null;
type StateHandler = (() => void) | null;

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  onopen: StateHandler = null;
  onerror: StateHandler = null;
  onmessage: MessageHandler = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  close() {
    this.closed = true;
  }

  open() {
    this.onopen?.();
  }

  fail() {
    this.onerror?.();
  }

  emit(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify(payload) });
  }

  emitRaw(data: string) {
    this.onmessage?.({ data });
  }
}

const FullTask = (overrides: Partial<CurrentTaskData> = {}): CurrentTaskData => ({
  taskId: 42,
  scanFinish: true,
  createTime: 1_700_000_000,
  duration: 12,
  num: { wait: 1, running: 2, success: 3, fail: 0, other: 0 },
  size: { wait: 10, running: 20, success: 30, fail: 0, other: 0 },
  doneSize: 30,
  remainSize: 30,
  speed: 100,
  doingTask: [
    { id: 1, fileName: "a.bin", status: 1, progress: 10 },
    { alistTaskId: "remote-2", fileName: "b.bin", status: 1, progress: 20 },
  ],
  ...overrides,
});

const envelope = (data: unknown, code = 200) => ({
  code,
  data,
  msg: code === 200 ? "success" : "failed",
});

function createHarness() {
  const frames: (CurrentTaskData | null)[] = [];
  const states: JobProgressStreamState[] = [];
  const stream = createJobProgressStream({
    jobId: "9",
    onData: (data) => frames.push(data),
    onStateChange: (state) => states.push(state),
  });
  const source = FakeEventSource.instances.at(-1)!;
  return { stream, source, frames, states };
}

describe("job progress SSE stream", () => {
  const original = globalThis.EventSource;
  beforeEach(() => {
    FakeEventSource.instances = [];
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
  });
  afterEach(() => {
    globalThis.EventSource = original;
  });

  it("builds the e2e contract URL with both id and jobId", () => {
    const url = jobProgressStreamUrl("12");
    expect(url).toContain("/app/opensync/svr/job/stream");
    expect(url).toContain("id=12");
    expect(url).toContain("jobId=12");
  });

  it("treats the first message as proof the stream is live (no onopen needed)", () => {
    const { source, frames, states } = createHarness();
    source.emit(envelope(FullTask()));
    expect(states.at(-1)?.live).toBe(true);
    expect(frames.at(-1)).toMatchObject({ taskId: 42 });
  });

  it("applies a full snapshot frame and reports the connection live", () => {
    const { source, frames, states } = createHarness();
    source.open();
    expect(states.at(-1)?.live).toBe(true);

    source.emit(envelope(FullTask()));
    expect(frames.at(-1)).toMatchObject({ taskId: 42, doneSize: 30 });
    expect(frames.at(-1)?.doingTask).toHaveLength(2);
  });

  it("merges a doingPatch frame into the previous doingTask list", () => {
    const { source, frames } = createHarness();
    source.open();
    source.emit(envelope(FullTask()));
    source.emit(
      envelope(
        FullTask({
          doingTask: null,
          doingPatch: [{ id: 1, status: 1, progress: 80 }],
        } as unknown as Partial<CurrentTaskData>),
      ),
    );
    const task = frames.at(-1)!;
    expect(task.doingTask).toHaveLength(2);
    expect(task.doingTask?.[0]).toMatchObject({ id: 1, progress: 80 });
    expect(task.doingTask?.[1]).toMatchObject({
      alistTaskId: "remote-2",
      progress: 20,
    });
  });

  it("accepts an explicit null snapshot as 'no task running'", () => {
    const { source, frames } = createHarness();
    source.open();
    source.emit(envelope(FullTask()));
    source.emit(envelope(null));
    expect(frames.at(-1)).toBeNull();
  });

  it("falls back (live=false) on error without reconnecting itself", () => {
    const { source, states } = createHarness();
    source.open();
    source.fail();
    expect(states.at(-1)).toEqual({ live: false, reason: "error" });
    // EventSource reconnects by itself: the client must not create another one.
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  it("ignores malformed frames and non-200 envelopes", () => {
    const { source, frames, states } = createHarness();
    source.open();
    source.emitRaw("not-json");
    expect(frames).toHaveLength(0);
    source.emit(envelope({ any: "thing" }, 500));
    expect(states.at(-1)).toEqual({ live: false, reason: "error" });
    expect(frames).toHaveLength(0);
  });

  it("closes the EventSource and stops delivering frames", () => {
    const { stream, source, frames } = createHarness();
    source.open();
    source.emit(envelope(FullTask()));
    const delivered = frames.length;
    stream.close();
    expect(source.closed).toBe(true);
    source.emit(envelope(FullTask({ taskId: 43 })));
    expect(frames).toHaveLength(delivered);
  });

  it("reports unsupported when EventSource is unavailable", () => {
    globalThis.EventSource = undefined as unknown as typeof EventSource;
    const frames: (CurrentTaskData | null)[] = [];
    const states: JobProgressStreamState[] = [];
    const stream = createJobProgressStream({
      jobId: "1",
      onData: (data) => frames.push(data),
      onStateChange: (state) => states.push(state),
    });
    expect(states.at(-1)).toEqual({ live: false, reason: "unsupported" });
    expect(frames).toHaveLength(0);
    stream.close();
  });
});

describe("progress frame reducer", () => {
  it("parses envelopes and rejects non-envelopes", () => {
    expect(parseProgressEnvelope(JSON.stringify(envelope(FullTask()))))?.toMatchObject(
      { code: 200 },
    );
    expect(parseProgressEnvelope("oops")).toBeNull();
    expect(parseProgressEnvelope(JSON.stringify({ data: [] }))).toBeNull();
  });

  it("keeps the previous view for an unknown payload shape", () => {
    const previous = FullTask();
    expect(applyProgressFrame(previous, { unexpected: true })).toBe(previous);
  });

  it("resets the doing list when the task identity changes", () => {
    const previous = FullTask();
    const next = applyProgressFrame(
      previous,
      FullTask({
        taskId: 99,
        doingTask: [{ id: 7, fileName: "new.bin", status: 1, progress: 5 }],
      }),
    );
    expect(next?.taskId).toBe(99);
    expect(next?.doingTask).toHaveLength(1);
  });

  it("does not let a patch from a newer task reuse the old rows", () => {
    const previous = FullTask();
    const next = applyProgressFrame(
      previous,
      FullTask({
        taskId: 99,
        doingTask: null,
        doingPatch: [{ id: 3, status: 1, progress: 50 }],
      } as unknown as Partial<CurrentTaskData>),
    );
    expect(next?.doingTask).toHaveLength(1);
    expect(next?.doingTask?.[0]).toMatchObject({ id: 3, progress: 50 });
  });
});
