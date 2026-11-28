package ingest

import (
	"cmp"
	"slices"
	"sync"
)

// rejections counts the rejected requests by Project and code, in memory,
// between two logs.
type rejections struct {
	mu     sync.Mutex
	counts map[rejection]int
}

// rejection is one Project and error code.
type rejection struct {
	ProjectID string
	Code      code
}

// rejectionCount is the number of rejections of a Project with a code, as
// logged.
type rejectionCount struct {
	ProjectID string `json:"project_id"`
	Code      code   `json:"code"`
	Count     int    `json:"count"`
}

func (r *rejections) add(projectID string, c code) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.counts == nil {
		r.counts = map[rejection]int{}
	}
	r.counts[rejection{ProjectID: projectID, Code: c}]++
}

// take returns the counts since the last call, by Project and code, and
// starts counting again.
func (r *rejections) take() []rejectionCount {
	r.mu.Lock()
	counts := r.counts
	r.counts = nil
	r.mu.Unlock()

	list := make([]rejectionCount, 0, len(counts))
	for key, n := range counts {
		list = append(list, rejectionCount{ProjectID: key.ProjectID, Code: key.Code, Count: n})
	}
	slices.SortFunc(list, func(a, b rejectionCount) int {
		return cmp.Or(cmp.Compare(a.ProjectID, b.ProjectID), cmp.Compare(a.Code, b.Code))
	})
	return list
}
