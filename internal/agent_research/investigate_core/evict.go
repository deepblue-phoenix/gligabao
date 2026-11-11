package investigate_core

import "slices"

// Evict drops from the window the turns older than the last keep turns, except those that hold a pending (uncommitted) result: the raw
// output of a committed turn is in the notes (or was found useless) and the attempt log keeps its query and a line of outcome, so nothing
// is left in its place. A whole turn goes (the call and its result), so the history stays well formed. Pure.
func Evict(ls LoopState, keep int) LoopState {
	keep = max(keep, 1)
	n := len(ls.Window)
	if n <= keep {
		return ls
	}
	held := func(t Turn) bool {
		return slices.ContainsFunc(ls.Pending, func(p Pending) bool { return p.Turn == t.N })
	}
	var kept []Turn
	for i, t := range ls.Window {
		if i >= n-keep || held(t) {
			kept = append(kept, t)
		}
	}
	ls.Window = kept
	return ls
}

// NeedsExtraction returns the pending result that makes the next turn a forced extraction: one above the threshold (characters), or one
// that would leave the window at the next eviction (it came keep turns ago or more, counting the turn about to be made). ok is false if
// there is none.
func NeedsExtraction(ls LoopState, threshold, keep int) (Pending, bool) {
	keep = max(keep, 1)
	i := slices.IndexFunc(ls.Pending, func(p Pending) bool {
		return p.Size > threshold || ls.Turns+1-p.Turn >= keep
	})
	if i < 0 {
		return Pending{}, false
	}
	return ls.Pending[i], true
}
