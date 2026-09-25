package ai

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// command runs a command profile: its command without a shell, with
// {prompt}, {image}, {model} and {effort} replaced in each argument
// ({image} empty for a run without one). A command without {prompt} gets
// the prompt on stdin instead. The answer is an Answer as JSON, or else
// the whole text as the body; ask, digest, filter, last, triage and url
// require JSON.
func (r *runner) command(ctx context.Context) (Result, error) {
	p := r.call.Profile
	prompt := joinPrompt(r.req.System, r.req.Text)
	image := ""
	if r.image != nil {
		var err error
		if image, err = r.writeImage(); err != nil {
			return Result{}, err
		}
	}
	// One pass over each argument: a prompt that holds "{image}" stays as
	// it is.
	values := strings.NewReplacer("{prompt}", prompt, "{image}", image, "{model}", p.Model, "{effort}", p.Effort)
	args := make([]string, 0, len(p.Command)-1)
	promptInArgs := false
	total := len(r.path) + 1
	for _, arg := range p.Command[1:] {
		expanded := int64(len(arg))
		for _, replacement := range [][2]string{{"{prompt}", prompt}, {"{image}", image}, {"{model}", p.Model}, {"{effort}", p.Effort}} {
			expanded += int64(strings.Count(arg, replacement[0])) * int64(len(replacement[1])-len(replacement[0]))
		}
		if expanded > maxArgumentBytes || int64(total)+expanded+1 > maxArgumentsBytes {
			return Result{}, fmt.Errorf("ai: expanded command arguments exceed the limit; pass the prompt on stdin")
		}
		total += int(expanded) + 1
		if strings.Contains(arg, "{prompt}") {
			promptInArgs = true
		}
		args = append(args, values.Replace(arg))
	}
	var stdin []byte
	if !promptInArgs {
		stdin = []byte(prompt)
	}

	name := r.name()
	stdout, stderr, err := run(ctx, process{name: name, path: r.path, args: args, stdin: stdin, dir: r.work}, p.Timeout)
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return Result{}, failure(name, err, lastLine(stderr))
	case err != nil:
		return Result{}, err
	}
	if result, ok := r.decodeResult(stdout); ok {
		return result, nil
	}
	if r.call.Task == "ask" || r.call.Task == "filter" || r.call.Task == "last" || r.call.Task == "triage" || r.call.Task == "url" || r.call.Task == "digest" {
		return Result{}, r.invalidReply()
	}
	return Result{Answer: Answer{Body: strings.TrimSpace(string(stdout))}}, nil
}
