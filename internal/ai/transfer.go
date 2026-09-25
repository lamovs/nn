package ai

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/lamovs/nn/internal/config"
)

const maxTransferBytes = 256 << 10

// TransferBinding ties a background approval to one job and its input.
type TransferBinding struct {
	JobID         string
	RequestSHA256 [32]byte
}

// RequestDigest hashes unambiguous, length-delimited request fields.
func RequestDigest(req Request) [32]byte {
	h := sha256.New()
	for _, data := range [][]byte{[]byte(req.System), []byte(req.Text), req.Image} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(data)))
		h.Write(size[:])
		h.Write(data)
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

type transferEnvelope struct {
	Version int
	Call    Call
	Binding TransferBinding
}

// WriteTransfer consumes a live approval and sends its immutable call over
// an inherited pipe. It never persists consent or creates a credential.
func WriteTransfer(w *os.File, approval Approval, binding TransferBinding) error {
	if err := transferPipe(w); err != nil {
		return err
	}
	if !validBinding(binding) {
		return fmt.Errorf("ai: invalid transfer binding")
	}
	call, ok := approval.allowed()
	if !ok || approval.grant.requestDigest != nil {
		return fmt.Errorf("ai: transfer %w", ErrNotApproved)
	}
	data, err := json.Marshal(transferEnvelope{Version: 1, Call: call, Binding: binding})
	if err != nil {
		return err
	}
	if len(data) > maxTransferBytes {
		return fmt.Errorf("ai: approval transfer too large")
	}
	if _, ok := grants.LoadAndDelete(approval.grant); !ok {
		return fmt.Errorf("ai: transfer %w", ErrNotApproved)
	}
	_, err = w.Write(data)
	return err
}

// ReadTransfer: parent/child transport, not a boundary against other
// programs running as the same user. Permits only the bound request.
func ReadTransfer(r *os.File, cfg config.Config, expected TransferBinding) (Approval, error) {
	if err := transferPipe(r); err != nil {
		return Approval{}, err
	}
	data, err := io.ReadAll(io.LimitReader(r, maxTransferBytes+1))
	if err != nil {
		return Approval{}, err
	}
	if len(data) > maxTransferBytes {
		return Approval{}, fmt.Errorf("ai: approval transfer too large")
	}
	var envelope transferEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Approval{}, fmt.Errorf("ai: invalid approval transfer: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Approval{}, fmt.Errorf("ai: trailing approval transfer data")
	}
	if envelope.Version != 1 || !validBinding(expected) || envelope.Binding != expected {
		return Approval{}, fmt.Errorf("ai: approval transfer binding mismatch")
	}
	call := envelope.Call
	if !slices.Contains([]string{"shot", "title", "url"}, call.Task) || call.Name == "" || call.Profile.Timeout <= 0 {
		return Approval{}, fmt.Errorf("ai: invalid transferred call")
	}
	switch call.Profile.Engine {
	case engineClaude, engineCodex:
	case config.EngineCommand:
		if slices.Contains(config.BuiltinProfiles(), call.Name) || len(call.Profile.Command) == 0 {
			return Approval{}, fmt.Errorf("ai: invalid transferred command profile")
		}
	default:
		return Approval{}, fmt.Errorf("ai: invalid transferred engine")
	}
	if call.Profile.Effort != "" && !slices.Contains(efforts, call.Profile.Effort) {
		return Approval{}, fmt.Errorf("ai: invalid transferred effort")
	}
	if err := checkCallPolicy(cfg, call); err != nil {
		return Approval{}, err
	}
	digest := expected.RequestSHA256
	return issueBound(call, &digest), nil
}

func validBinding(binding TransferBinding) bool {
	if binding.JobID == "" || len(binding.JobID) > 128 || binding.RequestSHA256 == ([32]byte{}) {
		return false
	}
	for _, c := range binding.JobID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func transferPipe(f *os.File) error {
	if f == nil {
		return fmt.Errorf("ai: approval transfer needs an inherited pipe")
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return fmt.Errorf("ai: approval transfer needs an inherited pipe")
	}
	return nil
}

// CheckApproval rechecks a live approval against fresh prohibitions without
// consuming it.
func CheckApproval(cfg config.Config, approval Approval) error {
	call, ok := approval.allowed()
	if !ok {
		return ErrNotApproved
	}
	return checkCallPolicy(cfg, call)
}
func checkCallPolicy(cfg config.Config, call Call) error {
	if cfg.AI.Tasks[call.Task].Run == "never" {
		return fmt.Errorf("ai.tasks.%s.run is never", call.Task)
	}
	key := ConsentKey(call.Name, call.Profile)
	if ConsentOf(cfg, key) == ConsentNever {
		return &ConsentError{Key: key}
	}
	return nil
}
