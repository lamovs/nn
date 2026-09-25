package snippet

import (
	"reflect"
	"testing"
)

func TestFind(t *testing.T) {
	got := Find("docker run --name {{name:app}} -p {{port:8080}}:80 {{image}}")
	want := []Placeholder{
		{Name: "name", Default: "app", HasDefault: true},
		{Name: "port", Default: "8080", HasDefault: true},
		{Name: "image", Default: "", HasDefault: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Find() = %+v, want %+v", got, want)
	}
}

func TestFindDedupesKeepingFirstDefault(t *testing.T) {
	got := Find("{{tag:first}} ... {{tag:second}}")
	want := []Placeholder{{Name: "tag", Default: "first", HasDefault: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Find() = %+v, want %+v", got, want)
	}
}

func TestFindNoPlaceholders(t *testing.T) {
	if got := Find("docker system prune -af"); got != nil {
		t.Fatalf("Find() = %+v, want nil", got)
	}
}

func TestMissing(t *testing.T) {
	code := "{{a}} {{b:def}} {{c}}"
	got := Missing(code, map[string]string{"c": "x"})
	want := []string{"a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Missing() = %v, want %v", got, want)
	}
}

func TestMissingNoneWhenAllHaveDefaultsOrValues(t *testing.T) {
	code := "{{a:1}} {{b}}"
	if got := Missing(code, map[string]string{"b": "2"}); got != nil {
		t.Fatalf("Missing() = %v, want nil", got)
	}
}

func TestRenderUsesValueOverDefault(t *testing.T) {
	got := Render("docker run {{name:app}}", map[string]string{"name": "web"})
	want := "docker run web"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderFallsBackToDefault(t *testing.T) {
	got := Render("docker run {{name:app}}", nil)
	want := "docker run app"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderLeavesMissingPlaceholderAsIs(t *testing.T) {
	got := Render("docker run {{name}}", nil)
	want := "docker run {{name}}"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderEveryOccurrence(t *testing.T) {
	got := Render("{{tag}}-a {{tag}}-b", map[string]string{"tag": "x"})
	want := "x-a x-b"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func TestRenderEmptyDefault(t *testing.T) {
	got := Render("prefix{{suffix:}}", nil)
	want := "prefix"
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}
