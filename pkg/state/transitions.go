package state

import (
	"context"
	"fmt"

	"github.com/jaimegago/petri/pkg/types"
)

// TransitionIfExpired is the lazy on-read expiry reconciler. When called with
// a lab whose Status is ACTIVE and whose TTL has elapsed, it transitions the
// lab to EXPIRED in the state DB and returns the updated record. For every
// other status (or for an ACTIVE lab still within its TTL), it returns the
// lab unchanged.
//
// This is the single shared entry point that CLI reader commands (info, list,
// serve --lab, …) call before rendering or acting on a lab's Status. It is
// safe to call concurrently — the worst case under a race is one redundant
// UPDATE that writes the same EXPIRED status the other writer just persisted.
// All in-tree lab Status transitions live alongside this helper (see ADR
// 0013).
//
// The lab pointer is mutated in place when the transition fires so callers
// that already hold the pointer see the new Status.
func TransitionIfExpired(ctx context.Context, store Manager, lab *types.Lab) (*types.Lab, error) {
	if lab == nil {
		return nil, nil
	}
	if lab.Status != types.LabStatusActive || !lab.IsExpired() {
		return lab, nil
	}
	lab.Status = types.LabStatusExpired
	if err := store.UpdateLab(ctx, lab); err != nil {
		return lab, fmt.Errorf("transitioning lab %s to EXPIRED: %w", lab.Name, err)
	}
	return lab, nil
}
