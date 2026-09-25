package noteai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lamovs/nn/internal/ai"
	"github.com/lamovs/nn/internal/app"
	"github.com/lamovs/nn/internal/config"
	"github.com/lamovs/nn/internal/platform"
	"github.com/lamovs/nn/internal/vault"
)

const maxJobBytes = ai.MaxSystemBytes + ai.MaxTextBytes + (128 << 10)
const startupTimeout = 10 * time.Second

var workerExecutable = os.Executable

type Job struct {
	Task       string
	AllowTitle bool
	Version    int
	ID         string
	Config     string
	Root       string
	Inbox      string
	Note       string
	Image      string
	System     string
	Text       string
}

func Start(ctx context.Context, dir string, j Job, approval ai.Approval, req ai.Request, executableResolvers ...func() (string, error)) error {
	resolveExecutable := workerExecutable
	if len(executableResolvers) > 0 {
		resolveExecutable = executableResolvers[0]
	}
	if j.Task == "" {
		j.Task = "shot"
	}
	call, err := approval.Call()
	if err != nil {
		return err
	}
	if call.Task != j.Task {
		return errors.New("AI job task does not match approval")
	}
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if len(data) > maxJobBytes {
		return errors.New("background metadata exceeds limit")
	}
	if err := os.WriteFile(filepath.Join(dir, "job.json"), data, 0600); err != nil {
		return err
	}
	executable, err := resolveExecutable()
	if err != nil {
		return err
	}
	grantRead, grantWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer grantRead.Close()
	defer grantWrite.Close()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyRead.Close()
	defer readyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()
	cmd := exec.Command(executable, "__ai", dir)
	cmd.ExtraFiles = []*os.File{grantRead, readyWrite, releaseRead}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Nil streams are /dev/null: no terminal or parent output inherited.
	if err := cmd.Start(); err != nil {
		return err
	}
	grantRead.Close()
	readyWrite.Close()
	releaseRead.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stop := func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	bound := ai.TransferBinding{JobID: j.ID, RequestSHA256: ai.RequestDigest(req)}
	sent := make(chan error, 1)
	go func() { err := ai.WriteTransfer(grantWrite, approval, bound); grantWrite.Close(); sent <- err }()
	ready := make(chan error, 1)
	go func() {
		var ack [6]byte
		_, err := io.ReadFull(readyRead, ack[:])
		if err == nil && string(ack[:]) != "READY\n" {
			err = errors.New("invalid worker acknowledgement")
		}
		ready <- err
	}()
	timer := time.NewTimer(startupTimeout)
	defer timer.Stop()
	fail := func(err error) error { grantWrite.Close(); readyRead.Close(); releaseWrite.Close(); stop(); return err }
	for sent != nil || ready != nil {
		select {
		case err := <-sent:
			if err != nil {
				return fail(fmt.Errorf("transfer approval: %w", err))
			}
			sent = nil
		case err := <-ready:
			if err != nil {
				return fail(fmt.Errorf("background worker did not accept the capture: %w", err))
			}
			ready = nil
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-timer.C:
			return fail(errors.New("background worker did not become ready"))
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	// One byte is an atomic release: a written byte commits ownership.
	n, err := releaseWrite.Write([]byte{'G'})
	if n != 1 {
		if err == nil {
			err = io.ErrShortWrite
		}
		return fail(err)
	}
	releaseWrite.Close()
	return nil
}

// Worker: the Approval arrives only on fd 3; fd 4 acknowledges a checked
// request and fd 5 releases it. No argument or environment value confers
// permission.
func Worker(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("private AI worker needs one job")
	}
	// Nonblocking, so Go's poller can interrupt reads on cancellation.
	for fd := 3; fd <= 5; fd++ {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			return errors.New("private AI worker needs inherited pipes")
		}
		if st.Mode&syscall.S_IFMT != syscall.S_IFIFO {
			return errors.New("private AI worker needs inherited pipes")
		}
		if err := syscall.SetNonblock(fd, true); err != nil {
			return err
		}
		syscall.CloseOnExec(fd)
	}
	transfer := os.NewFile(3, "approval")
	ready := os.NewFile(4, "ready")
	release := os.NewFile(5, "release")
	if transfer == nil || ready == nil || release == nil {
		return errors.New("private AI worker needs inherited pipes")
	}
	defer transfer.Close()
	defer ready.Close()
	defer release.Close()
	if info, err := ready.Stat(); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("private AI worker needs readiness pipe")
	}
	if info, err := release.Stat(); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("private AI worker needs release pipe")
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	stopStartup := context.AfterFunc(startupCtx, func() { transfer.Close(); release.Close(); ready.Close() })
	defer cancelStartup()
	defer stopStartup()
	_ = transfer.SetReadDeadline(time.Now().Add(startupTimeout))
	dir, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("private AI job directory required")
	}
	data, err := readRegular(filepath.Join(dir, "job.json"), maxJobBytes)
	if err != nil {
		return err
	}
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	if j.Task == "" {
		j.Task = "shot"
	}
	if j.Version != 1 || j.ID != filepath.Base(dir) || !filepath.IsAbs(j.Root) || j.Note == "" || (j.Task != "shot" && j.Task != "title" && j.Task != "url") {
		return errors.New("invalid AI job")
	}
	var payload urlPayload
	if j.Task == "url" {
		if j.Image != "" {
			return errors.New("link summary job takes no image")
		}
		if payload, err = parseURLPayload(j.Text); err != nil {
			return err
		}
	}
	var imageData []byte
	if j.Image != "" {
		if j.Image != filepath.Base(j.Image) || !strings.HasPrefix(j.Image, "original.") {
			return errors.New("invalid AI job image")
		}
		imageData, err = readRegular(filepath.Join(dir, j.Image), ai.MaxImageBytes)
		if err != nil {
			return err
		}
		if j.Task == "shot" {
			if err := ai.ValidateShotImage(imageData); err != nil {
				return err
			}
		}
	} else if j.Task == "shot" {
		return errors.New("screenshot job needs an image")
	}
	req := ai.Request{System: j.System, Text: j.Text, Image: imageData}
	if err := ai.ValidateRequest(req); err != nil {
		return err
	}
	cfg, _, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Path != j.Config {
		return errors.New("AI job config path changed")
	}
	approval, err := ai.ReadTransfer(transfer, cfg, ai.TransferBinding{JobID: j.ID, RequestSHA256: ai.RequestDigest(req)})
	if err != nil {
		return err
	}
	transfer.Close()
	call, err := approval.Call()
	if err != nil {
		return err
	}
	if call.Task != j.Task {
		return errors.New("AI job task does not match approval")
	}
	cfg.Vault.Root = j.Root
	cfg.Vault.Inbox = j.Inbox
	v, err := vault.Open(cfg)
	if err != nil {
		return err
	}
	if _, err := v.Load(j.Note); err != nil {
		return err
	}
	env := &app.Env{Cfg: cfg, Vault: v}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := io.WriteString(ready, "READY\n"); err != nil {
		return err
	}
	ready.Close()
	_ = release.SetReadDeadline(time.Now().Add(startupTimeout))
	var goSignal [1]byte
	if _, err := io.ReadFull(release, goSignal[:]); err != nil {
		return err
	}
	if goSignal[0] != 'G' {
		return errors.New("invalid worker release")
	}
	release.Close()
	stopStartup()
	cancelStartup()
	var result ai.Result
	fresh, _, runErr := config.Load()
	if runErr == nil {
		runErr = ai.CheckApproval(fresh, approval)
	}
	if runErr == nil {
		result, runErr = ai.Run(ctx, approval, req)
	}
	if runErr == nil && j.Task == "shot" && AnswerBody(result.Answer) == "" {
		runErr = errors.New("model returned no analysis text")
	}
	if runErr == nil && j.Task == "url" && result.URL == nil {
		runErr = errors.New("model returned no link summary")
	}
	var enrichment vault.Enrichment
	if runErr != nil {
		recoveryPath := dir
		if j.Image != "" {
			recoveryPath = filepath.Join(dir, j.Image)
		}
		enrichment.Body = Recovery(j.Task, recoveryPath)
		writeStatus(dir, "Model analysis failed; original retained")
	} else {
		vocabulary, err := Vocabulary(ctx, env)
		if err != nil {
			vocabulary = nil
		}
		enrichment = vault.Enrichment{Title: Title(result.Title), Tags: Keywords(result.Tags, vocabulary), AllowTitle: j.AllowTitle}
		if j.Task == "shot" {
			enrichment.Body = AnswerBody(result.Answer)
		}
		answer, _ := json.Marshal(result.Answer)
		if j.Task == "url" {
			reply := *result.URL
			enrichment = vault.Enrichment{Title: urlTitle(reply), Body: urlSummary(reply, payload.Truncated), Tags: Keywords(reply.Tags, vocabulary), AllowTitle: j.AllowTitle}
			answer, _ = json.Marshal(reply)
		}
		if err := os.WriteFile(filepath.Join(dir, "answer.json"), answer, 0600); err != nil {
			writeStatus(dir, "Could not retain model answer before append; original retained")
		}
		if enrichment.Body != "" {
			_ = os.WriteFile(filepath.Join(dir, "answer.md"), []byte(enrichment.Body), 0600)
		}
	}
	if _, err := v.AppendEnrichment(j.Note, enrichment); err != nil {
		writeStatus(dir, "Could not append result; original and any answer retained")
		return err
	}
	// Hooks and notifications must not block the durable note update.
	after := context.Background()
	env.PostSave(after, j.Note, "append")
	title := "Note metadata ready"
	switch j.Task {
	case "shot":
		title = "Screenshot analysis ready"
	case "url":
		title = "Link summary ready"
	}
	if runErr != nil {
		title = "Note metadata failed"
		switch j.Task {
		case "shot":
			title = "Screenshot analysis failed"
		case "url":
			title = "Link summary failed"
		}
	} else {
		_ = os.RemoveAll(dir)
	}
	_ = platform.Notify(after, cfg, title, j.Note)
	return runErr
}

func readRegular(path string, limit int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > int64(limit) {
		return nil, errors.New("invalid private AI job file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if len(data) > limit {
		return nil, errors.New("AI job file exceeds limit")
	}
	return data, err
}
func writeStatus(dir, status string) {
	// Fixed messages exclude model output, credentials and prompt contents.
	if len(status) > 512 {
		status = status[:512]
	}
	data, _ := json.Marshal(struct {
		Status string `json:"status"`
	}{status})
	_ = os.WriteFile(filepath.Join(dir, "status.json"), data, 0600)
}
