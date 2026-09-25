package platform

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptureProcessGroupCancellation(t *testing.T) {
	// This is an explicitly selected shell fixture, never a desktop tool.
	marker := filepath.Join(t.TempDir(), "child-marker")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := executeCommand(ctx, command{
			name: "sh", args: []string{"-c", `(while :; do printf child > "$1"; done) & wait`, "fixture", marker},
			processGroup: true,
		})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("fixture child did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled screenshot process group did not exit")
	}
	// A surviving child would immediately overwrite this marker. This also
	// works on systems where a killed orphan briefly remains as a zombie.
	if err := os.WriteFile(marker, []byte("cancelled"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "cancelled" {
		t.Fatalf("descendant is still active: marker=%q err=%v", data, err)
	}
}
