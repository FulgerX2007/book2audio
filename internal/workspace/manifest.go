package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Status is the state of one unit of work.
type Status string

const (
	StatusPending Status = "pending"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Unit is the recorded state of one unit of work: a page, a chapter, a
// translation chunk, an audio segment or an output file.
type Unit struct {
	In        string `json:"in,omitempty"`         // hash of the input the result was made from
	Out       string `json:"out,omitempty"`        // result file, relative to the working folder
	OutSHA256 string `json:"out_sha256,omitempty"` // hash of the result as it was written
	Status    Status `json:"status"`
	Error     string `json:"error,omitempty"`
	OCR       bool   `json:"ocr,omitempty"`     // extract: the page text came from OCR
	Chapter   string `json:"chapter,omitempty"` // translate, synth: the chapter the unit belongs to
}

// Stage holds the units of one pipeline stage.
type Stage struct {
	Units map[string]*Unit `json:"units"`
}

// Counts summarises the units of a stage.
type Counts struct {
	Total  int
	Done   int
	Failed int
}

// Manifest is the persistent state of a book, stored in manifest.json. Its
// methods are safe for concurrent use; every change is written to disk
// before the method returns.
type Manifest struct {
	Version int               `json:"version"`
	Source  Source            `json:"source"`
	Stages  map[string]*Stage `json:"stages,omitempty"`

	// Force makes Protected report false, so hand-edited files are
	// overwritten (--force).
	Force bool `json:"-"`

	mu  sync.Mutex
	dir string
}

// LoadManifest reads manifest.json of the working folder dir.
func LoadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, manifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := &Manifest{dir: dir}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("%s повреждён: %w", path, err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("%s: версия манифеста %d не поддерживается (нужна %d)", path, m.Version, ManifestVersion)
	}
	return m, nil
}

// SetSource replaces the description of the source PDF.
func (m *Manifest) SetSource(src Source) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Source = src
	return m.save()
}

// GetSource returns the description of the source PDF.
func (m *Manifest) GetSource() Source {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Source
}

// NeedsWork reports whether the unit must be (re)computed for the input
// hash inHash. A unit is skipped when it is done, its result file exists and
// it was made from the same input.
//
// A failed unit whose result file has appeared on disk for the same input
// was completed by hand: it is recorded as done and skipped.
func (m *Manifest) NeedsWork(stage, unit, inHash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	u := m.unit(stage, unit)
	if u == nil || u.In != inHash || u.Out == "" {
		return true, nil
	}
	sum, err := HashFile(m.abs(u.Out))
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	switch u.Status {
	case StatusDone:
		return false, nil
	case StatusFailed:
		u.Status, u.Error, u.OutSHA256 = StatusDone, "", sum
		return false, m.save()
	}
	return true, nil
}

// MarkDone records that the unit was computed from u.In into the file
// outPath (absolute or relative to the working folder). Out, OutSHA256,
// Status and Error of u are set here; the other fields are stored as given.
func (m *Manifest) MarkDone(stage, unit string, u Unit, outPath string) error {
	rel, err := m.rel(outPath)
	if err != nil {
		return err
	}
	sum, err := HashFile(m.abs(rel))
	if err != nil {
		return fmt.Errorf("результат единицы %s/%s: %w", stage, unit, err)
	}
	u.Out, u.OutSHA256, u.Status, u.Error = rel, sum, StatusDone, ""

	m.mu.Lock()
	defer m.mu.Unlock()
	m.stage(stage).Units[unit] = &u
	return m.save()
}

// MarkFailed records that computing the unit from u.In failed with cause.
// outPath is where the result was expected; it is shown by status so the
// result can be supplied by hand.
func (m *Manifest) MarkFailed(stage, unit string, u Unit, outPath string, cause error) error {
	rel, err := m.rel(outPath)
	if err != nil {
		return err
	}
	u.Out, u.OutSHA256, u.Status, u.Error = rel, "", StatusFailed, cause.Error()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.stage(stage).Units[unit] = &u
	return m.save()
}

// Edited reports whether the result file of the unit was changed by hand:
// it exists and its content differs from what the stage wrote.
func (m *Manifest) Edited(stage, unit string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	u := m.unit(stage, unit)
	if u == nil || u.OutSHA256 == "" {
		return false, nil
	}
	sum, err := HashFile(m.abs(u.Out))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sum != u.OutSHA256, nil
}

// Protected reports whether the result file of the unit must not be
// overwritten: it was edited by hand and Force is not set.
func (m *Manifest) Protected(stage, unit string) (bool, error) {
	if m.Force {
		return false, nil
	}
	return m.Edited(stage, unit)
}

// Prune removes the units of the stage that are not in keep. The result
// files stay on disk.
func (m *Manifest) Prune(stage string, keep []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.Stages[stage]
	if st == nil {
		return nil
	}
	keepSet := make(map[string]bool, len(keep))
	for _, id := range keep {
		keepSet[id] = true
	}
	removed := false
	for id := range st.Units {
		if !keepSet[id] {
			delete(st.Units, id)
			removed = true
		}
	}
	if !removed {
		return nil
	}
	return m.save()
}

// Unit returns a copy of the recorded unit.
func (m *Manifest) Unit(stage, unit string) (Unit, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u := m.unit(stage, unit); u != nil {
		return *u, true
	}
	return Unit{}, false
}

// UnitIDs returns the sorted unit IDs of the stage.
func (m *Manifest) UnitIDs(stage string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.Stages[stage]
	if st == nil {
		return nil
	}
	ids := make([]string, 0, len(st.Units))
	for id := range st.Units {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Counts returns the number of units of the stage by status.
func (m *Manifest) Counts(stage string) Counts {
	m.mu.Lock()
	defer m.mu.Unlock()
	var c Counts
	if st := m.Stages[stage]; st != nil {
		for _, u := range st.Units {
			c.Total++
			switch u.Status {
			case StatusDone:
				c.Done++
			case StatusFailed:
				c.Failed++
			}
		}
	}
	return c
}

// unit returns the stored unit or nil. The caller holds m.mu.
func (m *Manifest) unit(stage, unit string) *Unit {
	if st := m.Stages[stage]; st != nil {
		return st.Units[unit]
	}
	return nil
}

// stage returns the stage, creating it when needed. The caller holds m.mu.
func (m *Manifest) stage(name string) *Stage {
	if m.Stages == nil {
		m.Stages = make(map[string]*Stage)
	}
	st := m.Stages[name]
	if st == nil {
		st = &Stage{}
		m.Stages[name] = st
	}
	if st.Units == nil {
		st.Units = make(map[string]*Unit)
	}
	return st
}

// save writes the manifest atomically. The caller holds m.mu.
func (m *Manifest) save() error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := WriteFileAtomic(filepath.Join(m.dir, manifestName), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("запись манифеста: %w", err)
	}
	return nil
}

func (m *Manifest) abs(rel string) string {
	return filepath.Join(m.dir, filepath.FromSlash(rel))
}

// rel converts a result path to the slash-separated form stored in the
// manifest and rejects paths outside the working folder.
func (m *Manifest) rel(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.dir, path)
	}
	rel, err := filepath.Rel(m.dir, path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("файл %s находится вне рабочей папки %s", path, m.dir)
	}
	return filepath.ToSlash(rel), nil
}
