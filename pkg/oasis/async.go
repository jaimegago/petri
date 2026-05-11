package oasis

import (
	"log/slog"

	"github.com/jaimegago/petri/pkg/asynctasks"
)

// asyncTasks tracks background goroutines spawned during request handling.
// It is a thin alias around the shared asynctasks.Tasks so the OASIS provider
// and the petri-serve lab reaper coordinate through the same primitive.
// See ADRs 0011 and 0013.
type asyncTasks = asynctasks.Tasks

func newAsyncTasks(log *slog.Logger) *asyncTasks {
	return asynctasks.New(log)
}
