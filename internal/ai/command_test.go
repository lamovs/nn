package ai

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestCommandSubstitutes(t *testing.T) {
	f := newFake(t)
	f.put("stdout", "plain answer\n")
	call := commandCall("fake-model", "--model={model}", "--effort", "{effort}", "--image={image}", "{prompt}", "<{prompt}|{model}>")
	call.Profile.Model, call.Profile.Effort = "llama3", "max"

	res, err := Run(context.Background(), issue(call), Request{System: "Sys {model}", Text: "look at {image}", Image: pngImage})
	if err != nil {
		t.Fatal(err)
	}
	argv := f.argv()
	image := f.get("image_path")
	prompt := "Sys {model}\n\nlook at {image}"
	want := []string{"--model=llama3", "--effort", "max", "--image=" + image, prompt, "<" + prompt + "|llama3>"}
	if !slices.Equal(argv, want) {
		t.Errorf("argv = %q\nwant   %q", argv, want)
	}
	if !strings.HasSuffix(image, "/image.png") || f.get("image") != string(pngImage) {
		t.Errorf("image %s holds %q", image, f.get("image"))
	}
	if got := f.get("stdin"); got != "" {
		t.Errorf("stdin = %q, want nothing with {prompt} in the arguments", got)
	}
	f.assertRanPrivately()
	if res.Body != "plain answer" || res.Title != "" || res.Tags != nil {
		t.Errorf("answer = %+v, want the text as the body", res.Answer)
	}
}

func TestCommandPromptOnStdin(t *testing.T) {
	f := newFake(t)
	f.put("stdout", `{"title":"T","tags":["a"],"body":"B","extra":1}`)
	res, err := Run(context.Background(), issue(commandCall("fake-model", "--image={image}")), Request{System: "Sys", Text: "note"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.argv(); !slices.Equal(got, []string{"--image="}) {
		t.Errorf("argv = %q", got)
	}
	if got := f.get("stdin"); got != "Sys\n\nnote" {
		t.Errorf("stdin = %q", got)
	}
	if res.Title != "T" || !slices.Equal(res.Tags, []string{"a"}) || res.Body != "B" {
		t.Errorf("answer = %+v", res.Answer)
	}
}

func TestCommandAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, stdout string
		want         Answer
	}{
		{"JSON answer", `  {"title":"T","body":"B"}` + "\n", Answer{Title: "T", Body: "B"}},
		{"prose", "Just prose.\n", Answer{Body: "Just prose."}},
		{"JSON of another kind", `["a","b"]`, Answer{Body: `["a","b"]`}},
		{"JSON string", `"quoted"`, Answer{Body: `"quoted"`}},
		{"object without answer keys", `{"answer":"42"}`, Answer{Body: `{"answer":"42"}`}},
		{"answer key of the wrong type", `{"title":["T"],"body":"B"}`, Answer{Body: `{"title":["T"],"body":"B"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.put("stdout", tc.stdout)
			res, err := Run(context.Background(), issue(commandCall("fake-model")), Request{Text: "T"})
			if err != nil {
				t.Fatal(err)
			}
			if res.Title != tc.want.Title || res.Body != tc.want.Body || !slices.Equal(res.Tags, tc.want.Tags) {
				t.Errorf("answer = %+v, want %+v", res.Answer, tc.want)
			}
		})
	}
}

func TestCommandWithoutImageTakesNoImage(t *testing.T) {
	f := newFake(t)
	call := commandCall("fake-model", "{prompt}", "{model}")
	if AcceptsImage(call.Profile) {
		t.Error("AcceptsImage = true for a command without {image}")
	}
	_, err := Run(context.Background(), issue(call), Request{Text: "T", Image: pngImage})
	if !errors.Is(err, ErrImageUnsupported) {
		t.Errorf("err = %v, want ErrImageUnsupported", err)
	}
	if f.has("argv") {
		t.Error("the command ran")
	}
}

func TestCommandFailures(t *testing.T) {
	f := newFake(t)
	f.put("stderr", "loading model\nerror: out of memory\n")
	f.put("exit", "3")
	_, err := Run(context.Background(), issue(commandCall("fake-model")), Request{Text: "T"})
	if err == nil || err.Error() != "fake-model failed (exit status 3): error: out of memory" {
		t.Errorf("err = %v", err)
	}
	if exit := (*exec.ExitError)(nil); !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Errorf("err = %v does not carry exit status 3", err)
	}

	f = newFake(t)
	f.put("stdout", "  \n")
	if _, err := Run(context.Background(), issue(commandCall("fake-model")), Request{Text: "T"}); err == nil || !strings.Contains(err.Error(), "empty answer") {
		t.Errorf("blank output: err = %v, want an empty answer", err)
	}

	_, err = Run(context.Background(), issue(commandCall("no-such-model", "{prompt}")), Request{Text: "T"})
	if !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "no-such-model is not installed") {
		t.Errorf("missing program: err = %v", err)
	}
	_, err = Run(context.Background(), issue(commandCall("./no/such/model")), Request{Text: "T"})
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("missing path: err = %v", err)
	}
}
