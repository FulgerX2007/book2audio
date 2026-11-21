package execx

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// needTool skips the test when a real tool is not installed.
func needTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not installed", name)
	}
}

func TestCmdString(t *testing.T) {
	cmd := Cmd{Name: "pdftotext", Args: []string{"-f", "1", "book.pdf", "-"}}
	if got, want := cmd.String(), "pdftotext -f 1 book.pdf -"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestOSRunnerSuccess(t *testing.T) {
	needTool(t, "sh")
	out, err := OSRunner{}.Run(context.Background(), Cmd{
		Name:  "sh",
		Args:  []string{"-c", `read line; printf '%s|%s' "$line" "$B2A_TEST"; echo ignored >&2`},
		Env:   []string{"B2A_TEST=from-env"},
		Stdin: strings.NewReader("from-stdin\n"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := string(out), "from-stdin|from-env"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestOSRunnerKeepsParentEnvironment(t *testing.T) {
	needTool(t, "sh")
	t.Setenv("B2A_PARENT", "inherited")
	for _, env := range [][]string{nil, {"B2A_OTHER=x"}} {
		out, err := OSRunner{}.Run(context.Background(), Cmd{
			Name: "sh", Args: []string{"-c", `printf '%s' "$B2A_PARENT"`}, Env: env,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if string(out) != "inherited" {
			t.Errorf("with Env %v the parent environment is lost: %q", env, out)
		}
	}
}

func TestOSRunnerFailure(t *testing.T) {
	needTool(t, "sh")

	t.Run("non-zero exit carries stderr", func(t *testing.T) {
		_, err := OSRunner{}.Run(context.Background(), Cmd{
			Name: "sh", Args: []string{"-c", "echo broken input >&2; exit 3"},
		})
		if exitErr, ok := errors.AsType[*exec.ExitError](err); !ok || exitErr.ExitCode() != 3 {
			t.Fatalf("error = %v, want an exit status 3", err)
		}
		if !strings.Contains(err.Error(), "broken input") || !strings.HasPrefix(err.Error(), "sh: ") {
			t.Errorf("error %q lacks the tool name or its stderr", err)
		}
	})
	t.Run("non-zero exit without stderr", func(t *testing.T) {
		_, err := OSRunner{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "exit 1"}})
		if err == nil || strings.HasSuffix(err.Error(), ": ") {
			t.Errorf("error = %q", err)
		}
	})
	t.Run("long stderr is truncated", func(t *testing.T) {
		_, err := OSRunner{}.Run(context.Background(), Cmd{
			Name: "sh", Args: []string{"-c", "head -c 100000 /dev/zero | tr '\\0' x >&2; echo END >&2; exit 1"},
		})
		if err == nil || len(err.Error()) > maxStderr+200 || !strings.HasSuffix(err.Error(), "END") {
			t.Errorf("error length = %d, want the tail of stderr only", len(err.Error()))
		}
	})
	t.Run("missing tool", func(t *testing.T) {
		_, err := OSRunner{}.Run(context.Background(), Cmd{Name: "b2a-no-such-tool"})
		if !errors.Is(err, ErrToolNotFound) || !strings.Contains(err.Error(), "b2a-no-such-tool") {
			t.Errorf("error = %v, want ErrToolNotFound naming the tool", err)
		}
	})
}

func TestOSRunnerCancellation(t *testing.T) {
	needTool(t, "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := OSRunner{}.Run(ctx, Cmd{Name: "sleep", Args: []string{"30"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the process was not stopped: Run took %v", elapsed)
	}
}

func TestCheck(t *testing.T) {
	needTool(t, "sh")
	if err := Check("sh"); err != nil {
		t.Errorf("Check(sh) = %v, want nil", err)
	}
	err := Check("b2a-no-such-tool")
	if !errors.Is(err, ErrToolNotFound) {
		t.Errorf("Check = %v, want ErrToolNotFound", err)
	}
	for tool, hint := range installHints {
		if _, lookErr := exec.LookPath(tool); lookErr == nil {
			continue
		}
		if err := Check(tool); err == nil || !strings.Contains(err.Error(), hint) {
			t.Errorf("Check(%s) = %v, want the install hint %q", tool, err, hint)
		}
	}
}

func TestFakeRunnerRecordsCalls(t *testing.T) {
	fake := NewFakeRunner()
	fake.Stub("pdfinfo", []byte("Pages: 3\n"), nil)
	fake.Handle("tesseract", func(call Call) ([]byte, error) {
		return []byte("text of " + call.Args[0]), nil
	})

	out, err := fake.Run(context.Background(), Cmd{Name: "pdfinfo", Args: []string{"book.pdf"}})
	if err != nil || string(out) != "Pages: 3\n" {
		t.Errorf("pdfinfo: out = %q, err = %v", out, err)
	}
	out, err = fake.Run(context.Background(), Cmd{
		Name:  "tesseract",
		Args:  []string{"page.png", "stdout"},
		Env:   []string{"OMP_THREAD_LIMIT=1"},
		Stdin: strings.NewReader("input"),
	})
	if err != nil || string(out) != "text of page.png" {
		t.Errorf("tesseract: out = %q, err = %v", out, err)
	}

	calls := fake.Calls()
	if len(calls) != 2 || calls[0].Name != "pdfinfo" || calls[1].Name != "tesseract" {
		t.Fatalf("calls = %+v, want pdfinfo then tesseract", calls)
	}
	got := calls[1]
	if !slices.Equal(got.Args, []string{"page.png", "stdout"}) ||
		!slices.Equal(got.Env, []string{"OMP_THREAD_LIMIT=1"}) ||
		string(got.Input) != "input" {
		t.Errorf("recorded call = %+v", got)
	}
	if n := len(fake.CallsTo("pdfinfo")); n != 1 {
		t.Errorf("CallsTo(pdfinfo) has %d calls, want 1", n)
	}
	if n := len(fake.CallsTo("ffmpeg")); n != 0 {
		t.Errorf("CallsTo(ffmpeg) has %d calls, want 0", n)
	}
}

func TestFakeRunnerErrors(t *testing.T) {
	fake := NewFakeRunner()
	boom := errors.New("exit status 1")
	fake.Stub("ffmpeg", nil, boom)

	if _, err := fake.Run(context.Background(), Cmd{Name: "ffmpeg"}); !errors.Is(err, boom) {
		t.Errorf("stubbed error = %v, want %v", err, boom)
	}
	if _, err := fake.Run(context.Background(), Cmd{Name: "piper"}); !errors.Is(err, ErrToolNotFound) {
		t.Errorf("unknown command error = %v, want ErrToolNotFound", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fake.Run(ctx, Cmd{Name: "ffmpeg"}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context error = %v, want context.Canceled", err)
	}
	if n := len(fake.Calls()); n != 2 {
		t.Errorf("recorded %d calls, want 2 (the cancelled one is not run)", n)
	}
}
