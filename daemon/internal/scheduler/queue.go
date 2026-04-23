package scheduler

import (
	"container/heap"
	"sync"
	"time"
)

const (
	initialBackoff = 1 * time.Minute
	maxBackoff     = 15 * time.Minute
	evictAfter     = 1 * time.Hour
)

// WatchItem represents a PR or issue being actively watched for changes.
type WatchItem struct {
	Type      string // "pr" | "issue"
	Repo      string
	Number    int
	GithubID  int64
	NextCheck time.Time
	Backoff   time.Duration
	LastSeen  time.Time // last detected activity

	index int // heap internal
}

// WatchQueue is a thread-safe priority queue ordered by NextCheck.
// The mutex protects the heap and seen map. Item field mutations (Backoff,
// NextCheck, LastSeen) happen outside the lock in ReEnqueue/ResetBackoff —
// this is safe because Tier 3 processes items sequentially (single writer).
// If concurrent callers ever mutate the same item, the fields should be
// moved inside the lock.
type WatchQueue struct {
	mu    sync.Mutex
	items watchHeap
	seen  map[int64]bool // dedup by GithubID
}

func NewWatchQueue() *WatchQueue {
	q := &WatchQueue{seen: make(map[int64]bool)}
	heap.Init(&q.items)
	return q
}

// Push adds an item to the queue. If the item (by GithubID) is already
// queued, it is silently ignored (dedup).
func (q *WatchQueue) Push(item *WatchItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.seen[item.GithubID] {
		return
	}
	if item.Backoff == 0 {
		item.Backoff = initialBackoff
	}
	if item.NextCheck.IsZero() {
		item.NextCheck = time.Now().Add(item.Backoff)
	}
	if item.LastSeen.IsZero() {
		item.LastSeen = time.Now()
	}
	q.seen[item.GithubID] = true
	heap.Push(&q.items, item)
}

// PopReady returns all items whose NextCheck is at or before now.
func (q *WatchQueue) PopReady() []*WatchItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	var ready []*WatchItem
	for q.items.Len() > 0 {
		peek := q.items[0]
		if peek.NextCheck.After(now) {
			break
		}
		item := heap.Pop(&q.items).(*WatchItem)
		delete(q.seen, item.GithubID)
		ready = append(ready, item)
	}
	return ready
}

// ReEnqueue puts an item back with doubled backoff (capped at maxBackoff).
//
// Concurrency contract: the caller must own the item exclusively between
// PopReady and ReEnqueue. The queue lock protects the heap and seen map;
// item field mutations happen inside the lock to avoid races with concurrent
// readers (e.g. Tier 3 logging a recently popped item's fields).
func (q *WatchQueue) ReEnqueue(item *WatchItem) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.seen[item.GithubID] {
		return
	}
	item.Backoff *= 2
	if item.Backoff > maxBackoff {
		item.Backoff = maxBackoff
	}
	item.NextCheck = time.Now().Add(item.Backoff)
	q.seen[item.GithubID] = true
	heap.Push(&q.items, item)
}

// ResetBackoff resets an item's backoff to initial and records the observed
// GitHub updated_at as LastSeen. Called when activity is detected on the
// item. Using observed (not time.Now()) is critical: if a Tier 3 check
// discovered the change via GitHub's updated_at, storing time.Now() as
// LastSeen would drift ahead of GitHub's clock and make subsequent ticks
// spuriously re-detect the same change. See theburrowhub/heimdallm#243.
//
// Same concurrency contract as ReEnqueue — caller must own the item
// exclusively.
func (q *WatchQueue) ResetBackoff(item *WatchItem, observedUpdatedAt time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.seen[item.GithubID] {
		return
	}
	item.Backoff = initialBackoff
	item.LastSeen = observedUpdatedAt
	item.NextCheck = time.Now().Add(initialBackoff)
	q.seen[item.GithubID] = true
	heap.Push(&q.items, item)
}

// Evict removes items that haven't had activity in over evictAfter.
func (q *WatchQueue) Evict() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	cutoff := time.Now().Add(-evictAfter)
	evicted := 0
	var keep watchHeap
	for _, item := range q.items {
		if item.LastSeen.Before(cutoff) {
			delete(q.seen, item.GithubID)
			evicted++
		} else {
			keep = append(keep, item)
		}
	}
	if evicted > 0 {
		q.items = keep
		heap.Init(&q.items)
	}
	return evicted
}

// Len returns the number of items in the queue.
func (q *WatchQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.items.Len()
}

// ── heap implementation ─────────────────────────────────────────────────

type watchHeap []*WatchItem

func (h watchHeap) Len() int           { return len(h) }
func (h watchHeap) Less(i, j int) bool { return h[i].NextCheck.Before(h[j].NextCheck) }
func (h watchHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *watchHeap) Push(x any) {
	item := x.(*WatchItem)
	item.index = len(*h)
	*h = append(*h, item)
}
func (h *watchHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*h = old[:n-1]
	return item
}
