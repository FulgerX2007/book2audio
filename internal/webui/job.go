package webui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/FulgerX2007/book2audio/internal/pipeline"
	"github.com/FulgerX2007/book2audio/internal/workspace"
)

// logLines is how many of the last log lines of a run are kept.
const logLines = 400

// job is one run of the pipeline started from the interface. The last job
// of a book is kept after it ends so that its outcome and log stay visible.
type job struct {
	cancel context.CancelFunc

	mu       sync.Mutex
	running  bool
	from, to string
	started  time.Time
	finished time.Time
	err      string
	stopped  bool
	lines    []string
	partial  []byte // an unfinished log line
}

// jobJSON is the state of a job as shown in the browser.
type jobJSON struct {
	Running  bool     `json:"running"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Started  string   `json:"started"`
	Finished string   `json:"finished,omitempty"`
	Error    string   `json:"error,omitempty"`
	Stopped  bool     `json:"stopped"`
	Log      []string `json:"log"`
}

func (j *job) snapshot() jobJSON {
	j.mu.Lock()
	defer j.mu.Unlock()
	snap := jobJSON{
		Running: j.running, From: j.from, To: j.to,
		Started: j.started.Format(time.RFC3339),
		Error:   j.err, Stopped: j.stopped,
		Log: append([]string{}, j.lines...),
	}
	if !j.finished.IsZero() {
		snap.Finished = j.finished.Format(time.RFC3339)
	}
	return snap
}

// Write collects the output of a run line by line; it implements io.Writer
// for the logger and for the stage summaries.
func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.partial = append(j.partial, p...)
	for {
		i := bytes.IndexByte(j.partial, '\n')
		if i < 0 {
			break
		}
		j.lines = append(j.lines, string(j.partial[:i]))
		j.partial = j.partial[i+1:]
	}
	if extra := len(j.lines) - logLines; extra > 0 {
		j.lines = append(j.lines[:0], j.lines[extra:]...)
	}
	return len(p), nil
}

func (j *job) finish(err error, stopped bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
	j.finished = time.Now()
	j.stopped = stopped
	if err != nil && !stopped {
		j.err = err.Error()
	}
}

// job returns the last job of a book, or nil.
func (s *Server) job(slug string) *job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[slug]
}

// start begins a run of a book in the background. Only one run per book
// can be in progress.
func (s *Server) start(slug string, opts pipeline.RunOptions, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.jobs[slug]; old != nil && old.snapshot().Running {
		return errors.New("the book is already being processed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{cancel: cancel, running: true, from: opts.From, to: opts.To, started: time.Now()}
	if s.jobs == nil {
		s.jobs = map[string]*job{}
	}
	s.jobs[slug] = j

	s.wg.Go(func() {
		defer cancel()
		err := s.run(ctx, slug, j, opts, force)
		stopped := ctx.Err() != nil
		j.finish(err, stopped)
		switch {
		case stopped:
			s.Log.Info("run stopped", "book", slug)
		case err != nil:
			s.Log.Warn("run failed", "book", slug, "err", err)
		default:
			s.Log.Info("run finished", "book", slug)
		}
	})
	return nil
}

// run executes the pipeline on a locked book and writes its log to the job.
func (s *Server) run(ctx context.Context, slug string, j *job, opts pipeline.RunOptions, force bool) error {
	ws, err := workspace.Open(filepath.Join(s.Workdir, slug), workspace.OpenOptions{})
	if err != nil {
		return err
	}
	if err := ws.Lock(); err != nil {
		return err
	}
	defer ws.Close()
	ws.Manifest.Force = force

	log := slog.New(slog.NewTextHandler(j, &slog.HandlerOptions{
		// The browser shows the lines as they are: drop the date, keep the time.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.String(slog.TimeKey, a.Value.Time().Format(time.TimeOnly))
			}
			return a
		},
	}))
	s.Log.Info("run started", "book", slug, "from", opts.From, "to", opts.To, "force", force)
	return pipeline.Run(ctx, ws, s.Deps, log, j, opts)
}
