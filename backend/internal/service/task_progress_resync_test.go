package service

import (
	"encoding/json"
	"opensync/internal/model"
	"testing"
)

// queueProgressPayload has to report an eviction, because that is the only
// signal that a client's next patch would apply to a frame it never received.
func TestQueueProgressPayloadReportsEviction(t *testing.T) {
	ch := make(chan []byte, 1)

	queued, dropped := queueProgressPayload(ch, []byte("first"))
	if !queued || dropped {
		t.Fatalf("first send: queued=%v dropped=%v, want queued without eviction", queued, dropped)
	}

	queued, dropped = queueProgressPayload(ch, []byte("second"))
	if !queued || !dropped {
		t.Fatalf("second send: queued=%v dropped=%v, want queued with eviction", queued, dropped)
	}
	if got := string(<-ch); got != "second" {
		t.Fatalf("buffered payload = %q, want the newest frame", got)
	}

	// A drained channel accepts a frame again without reporting a loss.
	queued, dropped = queueProgressPayload(ch, []byte("third"))
	if !queued || dropped {
		t.Fatalf("third send: queued=%v dropped=%v, want queued without eviction", queued, dropped)
	}
}

// A subscriber that lost a patch frame must be sent a full snapshot, not
// another diff: the diff is relative to the frame it never saw, so its progress
// bars would stay frozen and finished rows would never clear.
func TestPublishResyncsSubscriberThatLostAFrame(t *testing.T) {
	hub := newProgressHub()
	ch := make(chan []byte, 1)
	if !hub.subscribe(7, ch) {
		t.Fatal("subscribe() = false")
	}

	sub := hub.subscribers[7][ch]
	if sub.needsSnapshot {
		t.Fatal("a fresh subscriber must not start out needing a snapshot")
	}

	// Simulate the drop: a patch frame was evicted before the client read it.
	hub.setSubscriberNeedsSnapshot(7, sub, true)
	if !hub.subscriberNeedsSnapshot(7, sub) {
		t.Fatal("needsSnapshot did not stick")
	}

	frames := patchFramesFixture(t)
	snapshot, err := frames.snapshot()
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}

	// The resync frame must carry the whole doingTask list and no patch.
	var decoded struct {
		Data struct {
			DoingTask  []map[string]interface{} `json:"doingTask"`
			DoingPatch []map[string]interface{} `json:"doingPatch"`
		} `json:"data"`
	}
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		t.Fatalf("snapshot is not JSON: %v", err)
	}
	if len(decoded.Data.DoingTask) == 0 {
		t.Fatalf("resync frame has no doingTask: %s", snapshot)
	}
	if len(decoded.Data.DoingPatch) != 0 {
		t.Fatalf("resync frame must not carry a patch: %s", snapshot)
	}

	// Delivering it clears the flag, so the subscriber goes back to diffs.
	queued, dropped := queueProgressPayload(ch, snapshot)
	if !queued || dropped {
		t.Fatalf("resync send: queued=%v dropped=%v", queued, dropped)
	}
	hub.setSubscriberNeedsSnapshot(7, sub, false)
	if hub.subscriberNeedsSnapshot(7, sub) {
		t.Fatal("needsSnapshot was not cleared after a clean snapshot delivery")
	}
}

// The patch frame and the resync snapshot must describe the same observation,
// otherwise the resync would correct the client to a state the server does not
// hold as its diff baseline.
func TestProgressFramesSnapshotMatchesPatchObservation(t *testing.T) {
	frames := patchFramesFixture(t)
	if !frames.isPatch {
		t.Fatal("fixture did not produce a patch frame")
	}

	var patchFrame struct {
		Data struct {
			TaskID     int64                    `json:"taskId"`
			CreateTime int                      `json:"createTime"`
			DoingTask  []map[string]interface{} `json:"doingTask"`
			DoingPatch []map[string]interface{} `json:"doingPatch"`
		} `json:"data"`
	}
	if err := json.Unmarshal(frames.update, &patchFrame); err != nil {
		t.Fatalf("patch frame is not JSON: %v", err)
	}
	if len(patchFrame.Data.DoingPatch) == 0 {
		t.Fatalf("patch frame carries no patch: %s", frames.update)
	}
	if len(patchFrame.Data.DoingTask) != 0 {
		t.Fatalf("patch frame should omit doingTask: %s", frames.update)
	}

	snapshot, err := frames.snapshot()
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}
	var snapshotFrame struct {
		Data struct {
			TaskID     int64                    `json:"taskId"`
			CreateTime int                      `json:"createTime"`
			DoingTask  []map[string]interface{} `json:"doingTask"`
			DoingPatch []map[string]interface{} `json:"doingPatch"`
		} `json:"data"`
	}
	if err := json.Unmarshal(snapshot, &snapshotFrame); err != nil {
		t.Fatalf("snapshot is not JSON: %v", err)
	}
	if snapshotFrame.Data.TaskID != patchFrame.Data.TaskID ||
		snapshotFrame.Data.CreateTime != patchFrame.Data.CreateTime {
		t.Fatalf("snapshot identifies a different task than the patch it replaces:\npatch=%s\nsnapshot=%s",
			frames.update, snapshot)
	}
	if len(snapshotFrame.Data.DoingTask) == 0 {
		t.Fatalf("snapshot must carry the full doingTask list: %s", snapshot)
	}
	if len(snapshotFrame.Data.DoingPatch) != 0 {
		t.Fatalf("snapshot must not carry a patch: %s", snapshot)
	}
}

// A full frame needs no reconstruction: snapshot() returns it unchanged.
func TestProgressFramesSnapshotOfFullFrameIsTheFrameItself(t *testing.T) {
	frames := &progressFrames{update: []byte(`{"data":null}`), plain: true}
	got, err := frames.snapshot()
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}
	if string(got) != `{"data":null}` {
		t.Fatalf("snapshot() = %q, want the original frame", got)
	}
}

// patchFramesFixture drives prepareStreamPayload twice so the second frame is a
// patch, and returns it together with the snapshot view of the same state.
func patchFramesFixture(t *testing.T) *progressFrames {
	t.Helper()
	hub := newProgressHub()

	first := sampleCurrent(10)
	hub.prepareStreamPayload(99, &first, true)

	second := sampleCurrent(55)
	second.Duration = 5
	doingTask := second.DoingTask
	isPatch := hub.prepareStreamPayload(99, &second, false)
	payload, err := marshalProgressJSON(model.Success(second))
	if err != nil {
		t.Fatalf("marshalProgressJSON() error = %v", err)
	}
	return &progressFrames{
		update:    payload,
		isPatch:   isPatch,
		full:      second,
		doingTask: doingTask,
	}
}
