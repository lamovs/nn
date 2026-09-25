package shot

import (
	"context"
	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/noteai"
	"os"
)

var workerExecutable = os.Executable

type job = noteai.Job

func startWorker(ctx context.Context, dir string, j job, approval ai.Approval, req ai.Request) error {
	return noteai.Start(ctx, dir, j, approval, req, workerExecutable)
}

// Worker dispatches the private shared note metadata worker.
func Worker(ctx context.Context, args []string) error { return noteai.Worker(ctx, args) }
func writeStatus(dir, status string)                  { noteai.WriteStatus(dir, status) }
