package service

import (
	"bytes"
	"encoding/json"
	"log"
	"opensync/internal/model"
	"sync"
	"time"
)

const progressNotifyDebounce = 400 * time.Millisecond
const maxProgressSubscribersPerJob = 8

type progressItemState struct {
	progress   float64
	status     int
	generation uint64
}

// progressItemKey is deliberately a comparable value instead of a formatted
// string. Progress frames are rebuilt on every debounced SSE update; building
// "id:"/"alist:"/"path:" strings for every active item creates avoidable
// temporary allocations and CPU work on the hottest realtime path.
type progressItemKey struct {
	kind                       uint8
	id                         int64
	text                       string
	fileName, srcPath, dstPath string
}

type progressFrame struct {
	taskID     int64
	createTime int
	generation uint64
	keys       map[progressItemKey]progressItemState
}

// progressSubscriber tracks one SSE connection.
//
// needsSnapshot marks a client whose view can no longer be repaired by a diff:
// the outbound buffer holds a single frame, so a slow reader's queued frame is
// evicted to make room for a newer one. A patch describes the delta from the
// frame the client was supposed to have, so once one is dropped every later
// patch applies to a baseline that client never saw — progress bars freeze at
// stale values and completed rows never disappear. The next publish sends that
// subscriber a full snapshot instead.
type progressSubscriber struct {
	ch chan []byte
	// Guarded by progressHub.mu.
	needsSnapshot bool
}

type progressHub struct {
	// publishMu serializes whole publishes (build + dispatch) and new
	// subscriptions with their initial snapshot. Lock order: publishMu, then
	// mu / framesMu.
	publishMu   sync.Mutex
	mu          sync.Mutex
	framesMu    sync.Mutex
	subscribers map[int64]map[chan []byte]*progressSubscriber
	pending     map[int64]struct{}
	// timers holds the pending debounced publish of each job so it can be
	// stopped: a timer that outlived every viewer (or the process shutdown)
	// would publish after ShutdownDB. Guarded by mu.
	timers map[int64]*time.Timer
	frames map[int64]progressFrame
}

var jobProgressHub = newProgressHub()

func newProgressHub() *progressHub {
	return &progressHub{
		subscribers: make(map[int64]map[chan []byte]*progressSubscriber),
		pending:     make(map[int64]struct{}),
		timers:      make(map[int64]*time.Timer),
		frames:      make(map[int64]progressFrame),
	}
}

func SubscribeJobProgress(jobID int64) <-chan []byte {
	ch := make(chan []byte, 1)
	if !jobProgressHub.subscribe(jobID, ch) {
		return nil
	}
	return ch
}

func UnsubscribeJobProgress(jobID int64, ch <-chan []byte) {
	jobProgressHub.unsubscribe(jobID, ch)
}

func (h *progressHub) subscribe(jobID int64, ch chan []byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscribers[jobID] == nil {
		h.subscribers[jobID] = make(map[chan []byte]*progressSubscriber)
	}
	if len(h.subscribers[jobID]) >= maxProgressSubscribersPerJob {
		return false
	}
	h.subscribers[jobID][ch] = &progressSubscriber{ch: ch}
	return true
}

// StopJobProgressHub stops every pending debounced publish and drops the diff
// frames. Shutdown must call it before the database handle is closed: a timer
// that fires afterwards would read the closed handle (the persist flush timer is
// cancelled for the same reason). Subscribers are left alone — their request
// contexts end on shutdown and their deferred unsubscribe cleans them up.
func StopJobProgressHub() {
	jobProgressHub.stopAll()
}

func (h *progressHub) stopAll() {
	h.mu.Lock()
	timers := make([]*time.Timer, 0, len(h.timers))
	for jobID, timer := range h.timers {
		timers = append(timers, timer)
		delete(h.timers, jobID)
		delete(h.pending, jobID)
	}
	h.mu.Unlock()
	for _, timer := range timers {
		timer.Stop()
	}
	h.framesMu.Lock()
	h.frames = make(map[int64]progressFrame)
	h.framesMu.Unlock()
}

func (h *progressHub) unsubscribe(jobID int64, ch <-chan []byte) {
	clearFrame := false
	var pendingTimer *time.Timer
	h.mu.Lock()
	subs := h.subscribers[jobID]
	if subs == nil {
		h.mu.Unlock()
		return
	}
	for candidate := range subs {
		if candidate == ch {
			delete(subs, candidate)
			break
		}
	}
	if len(subs) == 0 {
		delete(h.subscribers, jobID)
		clearFrame = true
		// Nobody is left to receive the debounced frame, so stop the timer
		// instead of letting it publish after the last viewer left (and, at
		// shutdown, after ShutdownDB closed the database).
		pendingTimer = h.timers[jobID]
		delete(h.timers, jobID)
		delete(h.pending, jobID)
	}
	h.mu.Unlock()
	if pendingTimer != nil {
		pendingTimer.Stop()
	}
	// A disconnected viewer no longer needs the last progress diff frame.
	// Release it immediately so a job with many completed/aborted viewers does
	// not retain all active-item keys until the next task snapshot.
	if clearFrame {
		h.framesMu.Lock()
		delete(h.frames, jobID)
		h.framesMu.Unlock()
	}
}

func (jt *JobTask) notifyProgressChange() {
	if jt == nil || jt.JobClient == nil {
		return
	}
	jobProgressHub.schedule(jt.JobClient.idSnapshot())
}

func (jt *JobTask) notifyProgressNow() {
	if jt == nil || jt.JobClient == nil {
		return
	}
	jobProgressHub.publish(jt.JobClient.idSnapshot())
}

func (h *progressHub) schedule(jobID int64) {
	h.mu.Lock()
	if len(h.subscribers[jobID]) == 0 {
		h.mu.Unlock()
		return
	}
	if _, pending := h.pending[jobID]; pending {
		h.mu.Unlock()
		return
	}
	h.pending[jobID] = struct{}{}
	if h.timers == nil {
		h.timers = make(map[int64]*time.Timer)
	}
	timer := time.AfterFunc(progressNotifyDebounce, func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic in progressHub.schedule publish for job %d: %v", jobID, r)
			}
		}()

		h.mu.Lock()
		delete(h.pending, jobID)
		delete(h.timers, jobID)
		h.mu.Unlock()

		h.publish(jobID)
	})
	h.timers[jobID] = timer
	h.mu.Unlock()
}

func (h *progressHub) publish(jobID int64) {
	// Building and dispatching are one step. The debounced timer and
	// notifyProgressNow can publish concurrently; serialized only while
	// building, an older patch could still be queued after a newer frame and
	// leave the client on the older state.
	h.publishMu.Lock()
	defer h.publishMu.Unlock()
	subs := h.subscriberSnapshot(jobID)
	if len(subs) == 0 {
		return
	}

	frames, err := buildProgressFrames(jobID)
	if err != nil {
		log.Printf("Failed to marshal job %d progress stream payload: %v", jobID, err)
		return
	}
	h.dispatchFrames(jobID, subs, frames)
}

// dispatchFrames delivers one published observation to each subscriber, sending
// a full snapshot to any whose diff baseline is no longer trustworthy.
func (h *progressHub) dispatchFrames(jobID int64, subs []*progressSubscriber, frames *progressFrames) {
	for _, sub := range subs {
		// A subscriber that lost a frame gets the whole state instead of a diff
		// it could not apply. The flag is only cleared once the snapshot is
		// actually sitting in the buffer unevicted.
		if h.subscriberNeedsSnapshot(jobID, sub) {
			snapshot, err := frames.snapshot()
			if err != nil {
				log.Printf("Failed to marshal job %d progress snapshot: %v", jobID, err)
				continue
			}
			queued, dropped := queueProgressPayload(sub.ch, snapshot)
			if queued && !dropped {
				h.setSubscriberNeedsSnapshot(jobID, sub, false)
			}
			continue
		}

		queued, dropped := queueProgressPayload(sub.ch, frames.update)
		// Only a patch leaves the client dependent on the frame it just lost. A
		// dropped full snapshot is self-contained, so the newer one replacing it
		// is all the client needs.
		if frames.isPatch && (dropped || !queued) {
			h.setSubscriberNeedsSnapshot(jobID, sub, true)
		}
	}
}

func (h *progressHub) subscriberSnapshot(jobID int64) []*progressSubscriber {
	h.mu.Lock()
	defer h.mu.Unlock()

	subs := h.subscribers[jobID]
	if len(subs) == 0 {
		return nil
	}
	subscribers := make([]*progressSubscriber, 0, len(subs))
	for _, sub := range subs {
		subscribers = append(subscribers, sub)
	}
	return subscribers
}

func (h *progressHub) subscriberNeedsSnapshot(jobID int64, sub *progressSubscriber) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Re-read through the map: the subscriber may have unsubscribed since the
	// snapshot was taken, and a stale pointer must not resurrect it.
	current, ok := h.subscribers[jobID][sub.ch]
	if !ok {
		return false
	}
	return current.needsSnapshot
}

func (h *progressHub) setSubscriberNeedsSnapshot(jobID int64, sub *progressSubscriber, needs bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if current, ok := h.subscribers[jobID][sub.ch]; ok {
		current.needsSnapshot = needs
	}
}

// queueProgressPayload puts payload in ch, evicting an unread frame if the
// buffer is full. It reports whether the payload was queued, and whether doing
// so discarded a frame the client had not read yet.
func queueProgressPayload(ch chan []byte, payload []byte) (queued, dropped bool) {
	select {
	case ch <- payload:
		return true, false
	default:
	}
	select {
	case <-ch:
		dropped = true
	default:
	}
	select {
	case ch <- payload:
		return true, dropped
	default:
		// The reader took the slot between the eviction and this send, so its
		// view is still whatever it just read — older than this payload.
		return false, dropped
	}
}

// marshalJobSnapshot renders the job's current state as a self-contained
// frame WITHOUT touching the shared diff baseline. The baseline describes what
// the existing subscribers were last sent; overwriting it with a frame only the
// new connection received would make the next patch skip every file-set change
// that happened in between, for everyone else.
func marshalJobSnapshot(jobID int64) ([]byte, error) {
	data, err := GetJobCurrent(jobID, map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	return marshalProgressJSON(model.Success(data))
}

// progressFrames holds the frame to publish plus the means to render the same
// observation as a full snapshot, for a subscriber that cannot apply a diff.
//
// Both come from ONE call to GetJobCurrent. Observing the job twice would let
// the diff baseline and the resync snapshot describe different states, and the
// client would then be corrected to a state the server no longer treats as its
// baseline — the exact desync this resync exists to repair.
type progressFrames struct {
	update  []byte
	isPatch bool

	full      jobCurrentPayload
	doingTask []streamDoingItem
	plain     bool
}

// snapshot renders the observation as a self-contained frame.
func (f *progressFrames) snapshot() ([]byte, error) {
	if f.plain || !f.isPatch {
		// Already a full frame; nothing to reconstruct.
		return f.update, nil
	}
	full := f.full
	full.DoingTask = f.doingTask
	full.DoingPatch = nil
	return marshalProgressJSON(model.Success(full))
}

func buildProgressFrames(jobID int64) (*progressFrames, error) {
	data, err := GetJobCurrent(jobID, map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	if data == nil {
		jobProgressHub.clearFrame(jobID)
		payload, err := marshalProgressJSON(model.Success(nil))
		return &progressFrames{update: payload, plain: true}, err
	}
	current, ok := data.(jobCurrentPayload)
	if !ok {
		payload, err := marshalProgressJSON(model.Success(data))
		return &progressFrames{update: payload, plain: true}, err
	}

	// Captured before prepareStreamPayload clears DoingTask to emit a patch.
	doingTask := current.DoingTask
	isPatch := jobProgressHub.prepareStreamPayload(jobID, &current, false)
	payload, err := marshalProgressJSON(model.Success(current))
	if err != nil {
		// The baseline above already advanced to this observation. Drop it so
		// the next publish renders a full snapshot: a patch is a delta from a
		// frame the subscribers never received.
		jobProgressHub.clearFrame(jobID)
		return nil, err
	}
	return &progressFrames{
		update:    payload,
		isPatch:   isPatch,
		full:      current,
		doingTask: doingTask,
	}, nil
}

var progressJSONPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func marshalProgressJSON(v any) ([]byte, error) {
	buf := progressJSONPool.Get().(*bytes.Buffer)
	buf.Reset()
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		progressJSONPool.Put(buf)
		return nil, err
	}
	data := buf.Bytes()
	if n := len(data); n > 0 && data[n-1] == '\n' {
		data = data[:n-1]
	}
	out := make([]byte, len(data))
	copy(out, data)
	progressJSONPool.Put(buf)
	return out, nil
}

// BuildJobProgressStreamPayload renders a full snapshot of the job's progress
// without affecting the stream's diff baseline.
func BuildJobProgressStreamPayload(jobID int64) ([]byte, error) {
	return marshalJobSnapshot(jobID)
}

// SubscribeJobProgressWithSnapshot registers a stream subscriber and returns
// the first frame it must write. Both happen under the hub's publish lock:
//
//   - Built before subscribing, a publish landing in between (e.g. the task's
//     final frame) reached nobody and the client stayed on the stale state.
//     Under the lock every publish is either reflected in the snapshot or
//     delivered to the new channel.
//   - The new subscriber starts with needsSnapshot, because the snapshot was
//     not recorded as the shared baseline: a patch computed against that
//     baseline would not apply to it. Its next frame is therefore a full one.
//
// ok=false means the per-job subscriber limit was reached; err is a failure to
// build the snapshot (no subscription is left behind in either case).
func SubscribeJobProgressWithSnapshot(jobID int64) (updates <-chan []byte, initial []byte, ok bool, err error) {
	return jobProgressHub.subscribeWithSnapshot(jobID, func() ([]byte, error) {
		return marshalJobSnapshot(jobID)
	})
}

func (h *progressHub) subscribeWithSnapshot(jobID int64, build func() ([]byte, error)) (<-chan []byte, []byte, bool, error) {
	h.publishMu.Lock()
	defer h.publishMu.Unlock()
	initial, err := build()
	if err != nil {
		return nil, nil, true, err
	}
	ch := make(chan []byte, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subscribers[jobID]) >= maxProgressSubscribersPerJob {
		return nil, nil, false, nil
	}
	if h.subscribers[jobID] == nil {
		h.subscribers[jobID] = make(map[chan []byte]*progressSubscriber)
	}
	h.subscribers[jobID][ch] = &progressSubscriber{ch: ch, needsSnapshot: true}
	return ch, initial, true, nil
}

func (item streamDoingItem) streamKey() progressItemKey {
	if item.ID != 0 {
		return progressItemKey{kind: 1, id: item.ID}
	}
	if item.AlistTaskID != "" {
		return progressItemKey{kind: 2, text: item.AlistTaskID}
	}
	return progressItemKey{
		kind: 3,
		// Path-keyed items are only used before AList returns a remote task id.
		// Keep the common ID forms allocation-free and retain the fallback as
		// separate comparable fields so it also avoids composite-string builds.
		fileName: item.FileName,
		srcPath:  item.SrcPath,
		dstPath:  item.DstPath,
	}
}

func (item streamDoingItem) streamState() progressItemState {
	return progressItemState{
		progress: item.Progress,
		status:   item.Status,
	}
}

func (item streamDoingItem) toPatch(includePaths bool) streamDoingPatch {
	patch := streamDoingPatch{
		ID:          item.ID,
		AlistTaskID: item.AlistTaskID,
		Status:      item.Status,
		Progress:    item.Progress,
	}
	if includePaths {
		patch.FileName = item.FileName
		patch.SrcPath = item.SrcPath
		patch.DstPath = item.DstPath
	}
	return patch
}

// prepareStreamPayload converts payload into either a full snapshot or a patch
// relative to the previous frame, and reports whether it produced a patch.
func (h *progressHub) prepareStreamPayload(jobID int64, payload *jobCurrentPayload, snapshot bool) bool {
	patch, isPatch := h.updateFrame(jobID, payload, snapshot)
	if !isPatch {
		return false
	}
	payload.DoingTask = nil
	payload.DoingPatch = patch
	return true
}

// updateFrame records payload's doing set as the shared diff baseline and
// returns the patch that turns the previous baseline into this observation (nil
// when a full snapshot must be sent).
//
// The lock is released by defer: a panic while rebuilding the frame would
// otherwise leave framesMu locked forever, and every publish and every new SSE
// connection would block on it.
func (h *progressHub) updateFrame(jobID int64, payload *jobCurrentPayload, snapshot bool) ([]streamDoingPatch, bool) {
	doing := payload.DoingTask
	patch := make([]streamDoingPatch, 0, len(doing))

	h.framesMu.Lock()
	defer h.framesMu.Unlock()
	if h.frames == nil {
		h.frames = make(map[int64]progressFrame)
	}
	frame, hasPrev := h.frames[jobID]
	previousTaskID := frame.taskID
	previousCreateTime := frame.createTime
	sameTask := hasPrev && previousTaskID == payload.TaskID && previousCreateTime == payload.CreateTime
	if frame.keys == nil {
		frame.keys = make(map[progressItemKey]progressItemState, len(doing))
	}
	frame.taskID = payload.TaskID
	frame.createTime = payload.CreateTime
	frame.generation++
	if frame.generation == 0 {
		// A running task will never realistically reach uint64 wraparound, but
		// resetting the map keeps the generation invariant explicit.
		clear(frame.keys)
		frame.generation = 1
	}
	generation := frame.generation
	fileSetChanged := !sameTask
	for _, item := range doing {
		key := item.streamKey()
		state := item.streamState()
		previous, exists := frame.keys[key]
		if sameTask {
			if !exists {
				fileSetChanged = true
			} else if previous.progress != state.progress || previous.status != state.status {
				patch = append(patch, item.toPatch(item.ID == 0 && item.AlistTaskID == ""))
			}
		}
		state.generation = generation
		frame.keys[key] = state
	}
	for key, state := range frame.keys {
		if state.generation != generation {
			delete(frame.keys, key)
			if sameTask {
				fileSetChanged = true
			}
		}
	}
	h.frames[jobID] = frame

	if snapshot || !hasPrev || !sameTask || fileSetChanged {
		return nil, false
	}
	return patch, true
}

func (h *progressHub) clearFrame(jobID int64) {
	h.framesMu.Lock()
	delete(h.frames, jobID)
	h.framesMu.Unlock()
}
