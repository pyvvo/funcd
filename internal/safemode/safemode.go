// Package safemode counts funcd's unclean starts in <storage.dataDir>/safemode.json and turns a crash loop into a held
// start, then a stop that names the operator's restore (ADR-0207 Decisions 3 to 5). It records the last funcd upgrade,
// whose pre-upgrade generation is the way back.
package safemode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/version"
)

// StateFile is the start counter in the data directory.
const StateFile = "safemode.json"

// ExitStopped is the exit status of a stopped start; both units carry RestartPreventExitStatus=70.
const ExitStopped = 70

// MarkerReason is the hold marker's reason when safe mode holds the platform.
const MarkerReason = "safe-mode"

// Upgrade is the last funcd upgrade: the versions and the pre-upgrade generation, nil after --no-snapshot.
type Upgrade struct {
	From       string         `json:"from"`
	To         string         `json:"to"`
	Generation *backup.GenRef `json:"generation,omitempty"`
	At         v1.Timestamp   `json:"at"`
}

// State is StateFile: the unclean starts in a row, the version of the last start, the error the last failed start
// returned, and the last upgrade.
type State struct {
	Unclean   int      `json:"unclean"`
	Version   string   `json:"version,omitempty"`
	LastError string   `json:"lastError,omitempty"`
	Upgrade   *Upgrade `json:"upgrade,omitempty"`
}

// Config is recovery.safeMode, parsed: AfterCrashes at least 1.
type Config struct {
	AfterCrashes int
	StableAfter  time.Duration
}

// Mode is how a start goes on (Decision 4).
type Mode int

const (
	Normal Mode = iota
	Held
	Stopped
)

// StoppedError is a stopped start: it names the count, the last error and the next command.
type StoppedError struct{ State State }

func (e *StoppedError) Error() string {
	s := e.State
	var b strings.Builder
	fmt.Fprintf(&b, "safe mode: %d unclean starts in a row, so funcd stops (exit status %d) and opens no store", s.Unclean, ExitStopped)
	if s.LastError != "" {
		fmt.Fprintf(&b, "; the last start failed with: %s", s.LastError)
	}
	self := self()
	steps := "stop the unit; move the directories of storage.metastoreDir, workflow.dataDir and eventing.deadletter.dataDir aside; "
	u := s.Upgrade
	switch {
	case u != nil && u.To == version.Version && u.Generation != nil:
		fmt.Fprintf(&b, ". Roll back to funcd %s: %s`%s.previous restore run %s/%d`; install %s.previous at %s; "+
			"`funcd safe-mode reset`; start funcd (held); verify; `funcdctl hold release`", u.From, steps, self,
			u.Generation.Timeline, u.Generation.Generation, self, self)
	default:
		if u != nil && u.To == version.Version {
			fmt.Fprintf(&b, ". The upgrade from funcd %s wrote no pre-upgrade generation (--no-snapshot): no way back to it", u.From)
		}
		fmt.Fprintf(&b, ". Restore the newest verified generation: %s`%s restore run verified`; `funcd safe-mode reset`; "+
			"start funcd (held); verify; `funcdctl hold release`", steps, self)
	}
	return b.String()
}

// self is the running binary, symlinks resolved, as funcd upgrade names it; "<path>" when it does not resolve.
func self() string {
	p, err := os.Executable()
	if err == nil {
		p, err = filepath.EvalSymlinks(p)
	}
	if err != nil {
		return "<path>"
	}
	return p
}

// Start is one counted start: Clean and Failed record how it ended.
type Start struct {
	dataDir string
	mu      sync.Mutex
	state   State
}

// Begin reads StateFile and returns the state before this start and the mode: unclean below AfterCrashes is Normal,
// below twice it Held, else Stopped with a nil *Start and a *StoppedError, nothing written. Otherwise the start is
// recorded unclean under version. An absent file is the zero state; one that does not read or decode is
// fault.Internal, and no start is recorded.
func Begin(dataDir, version string, cfg Config) (*Start, State, Mode, error) {
	const op = "safemode.Begin"
	if cfg.AfterCrashes < 1 {
		return nil, State{}, Normal, fault.Invalidf(op, "recovery.safeMode.afterCrashes is %d: want at least 1", cfg.AfterCrashes)
	}
	before, err := read(op, dataDir)
	if err != nil {
		return nil, State{}, Normal, err
	}
	mode := Normal
	switch n := before.Unclean; {
	case n >= 2*cfg.AfterCrashes:
		return nil, before, Stopped, &StoppedError{State: before}
	case n >= cfg.AfterCrashes:
		mode = Held
	}
	s := &Start{dataDir: dataDir, state: before}
	s.state.Unclean++
	s.state.Version = version
	if err := write(op, dataDir, s.state); err != nil {
		return nil, State{}, Normal, err
	}
	return s, before, mode, nil
}

// Clean records the start clean: unclean = 0. A nil Start records nothing.
func (s *Start) Clean() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Unclean = 0
	return write("safemode.Clean", s.dataDir, s.state)
}

// Failed keeps err as lastError; the start stays as Begin or Clean left it. A nil Start records nothing.
func (s *Start) Failed(err error) error {
	if s == nil || err == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastError = err.Error()
	return write("safemode.Failed", s.dataDir, s.state)
}

// Reset sets unclean and lastError to zero and keeps the upgrade: `funcd safe-mode reset` (Decision 5). The hold
// marker stays, so the next start is held.
func Reset(dataDir string) error {
	const op = "safemode.Reset"
	s, err := read(op, dataDir)
	if err != nil {
		return err
	}
	s.Unclean, s.LastError = 0, ""
	return writeOwned(op, dataDir, s)
}

// RecordUpgrade records u as the last upgrade and the count as clean (Decision 1 step 6).
func RecordUpgrade(dataDir string, u Upgrade) error {
	const op = "safemode.RecordUpgrade"
	s, err := read(op, dataDir)
	if err != nil {
		return err
	}
	s.Upgrade, s.Unclean = &u, 0
	return writeOwned(op, dataDir, s)
}

func read(op, dataDir string) (State, error) {
	var s State
	data, err := os.ReadFile(filepath.Join(dataDir, StateFile)) //nolint:gosec // the data directory's own file
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return s, fault.Wrapf(err, fault.Internal, op, "read %s", filepath.Join(dataDir, StateFile))
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fault.Wrapf(err, fault.Internal, op, "decode %s", filepath.Join(dataDir, StateFile))
	}
	return s, nil
}

func write(op, dataDir string, s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "encode %s", StateFile)
	}
	return hold.WriteFile(op, dataDir, StateFile, data)
}

// writeOwned writes as root may, for funcd upgrade and safe-mode reset: the file then takes the data directory's
// owner (Decision 1, Owner).
func writeOwned(op, dataDir string, s State) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create %s", dataDir)
	}
	if err := write(op, dataDir, s); err != nil {
		return err
	}
	return hold.Own(dataDir, filepath.Join(dataDir, StateFile))
}
