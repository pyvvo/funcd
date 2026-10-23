//go:build dev

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/pflag"
	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/pyvvo/funcd/api/fault"
)

// devConfigFile is the file that holds what `funcdctl dev` is run with (ADR-0254 Decision 1). Its name is not
// funcdctl.yaml, which ADR-0247 Decision 11 refuses.
const devConfigFile = "funcdctl.config.yaml"

// Dev's own cadences (ADR-0254 Decision 4), in both modes; production's are 2s, 15s and 10s.
const (
	devHandOutSettle    = 250 * time.Millisecond
	devBlobPollInterval = time.Second
	devSegmentMaxAge    = time.Second
	// devVolumeMeasureInterval paces dev's volume measurement (ADR-0262 Decision 10): dev cannot limit a volume, so it
	// warns within this long of a volume passing its size.
	devVolumeMeasureInterval = 30 * time.Second
)

// devFileKeys are the keys under dev (ADR-0254 Contracts), dev's flags in camelCase.
type devFileKeys struct {
	Dir       string            `yaml:"dir"`
	Values    []string          `yaml:"values"`
	Build     []string          `yaml:"build"`
	Hooks     bool              `yaml:"hooks"`
	Apply     []string          `yaml:"apply"`
	Persist   bool              `yaml:"persist"`
	PersistTo string            `yaml:"persistTo"`
	Gport     int               `yaml:"gport"`
	S3port    int               `yaml:"s3port"`
	Cport     int               `yaml:"cport"`
	Env       map[string]string `yaml:"env"`
	EnvFile   string            `yaml:"envFile"`
	Platform  devFilePlatform   `yaml:"platform"`
	// Watch and LiveReload are App-only keys of the file alone (ADR-0255 Decision 1).
	Watch      []devWatchEntry `yaml:"watch"`
	LiveReload bool            `yaml:"liveReload"`
}

// devWatchEntry is a watch command dev starts and supervises (ADR-0255 Contracts).
type devWatchEntry struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
	Dir  string `yaml:"dir"`
}

// devFilePlatform holds the three FuncdConfig keys dev takes, in FuncdConfig's syntax (ADR-0254 Decision 4).
type devFilePlatform struct {
	Runtime struct {
		HandOutSettle string `yaml:"handOutSettle"`
	} `yaml:"runtime"`
	Eventing struct {
		BlobPollInterval string `yaml:"blobPollInterval"`
	} `yaml:"eventing"`
	Funclog struct {
		SegmentMaxAge string `yaml:"segmentMaxAge"`
	} `yaml:"funclog"`
	// Volumes paces dev's measurement of each Function volume against its size (ADR-0262 Decision 10).
	Volumes struct {
		MeasureInterval string `yaml:"measureInterval"`
	} `yaml:"volumes"`
}

// envFileKeyFormat is an envFile line's KEY: an environment variable name.
var envFileKeyFormat = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// watchNameFormat is a watch command's name, which names its log file (ADR-0255 Contracts).
var watchNameFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// devFileKeyNames are the keys dev takes.
func devFileKeyNames() []string {
	return []string{"dir", "values", "build", "hooks", "apply", "persist", "persistTo", "gport", "s3port", "cport", "env", "envFile",
		"platform", "watch", "liveReload"}
}

// devPlatformKeys are the FuncdConfig keys platform takes (Decision 4).
func devPlatformKeys() []string {
	return []string{"runtime.handOutSettle", "eventing.blobPollInterval", "funclog.segmentMaxAge", "volumes.measureInterval"}
}

// devReloadKeys are the keys whose change reloads; any other needs a restart (Decision 6).
func devReloadKeys() []string { return []string{"build", "values", "apply", "env"} }

// devPace is the cadences dev's platform runs with.
type devPace struct {
	handOutSettle, blobPollInterval, segmentMaxAge, volumeMeasure time.Duration
}

// defaultDevPace is Decision 4's defaults.
func defaultDevPace() devPace {
	return devPace{handOutSettle: devHandOutSettle, blobPollInterval: devBlobPollInterval, segmentMaxAge: devSegmentMaxAge,
		volumeMeasure: devVolumeMeasureInterval}
}

// devSecretSources are where a declared Secret that no document defines takes each key's value (ADR-0254 Decision 5):
// the shell's environment, else envFile's pairs, else env.
type devSecretSources struct {
	envFile     string // the envFile path as the file names it, "" when it names none
	envFileVals map[string]string
	env         map[string]string
}

// lookup is key's value from the first source that holds it.
func (s devSecretSources) lookup(key string) (string, bool) {
	if v, ok := os.LookupEnv(key); ok {
		return v, true
	}
	if v, ok := s.envFileVals[key]; ok {
		return v, true
	}
	v, ok := s.env[key]
	return v, ok
}

// missing is the error for a key no source holds; who names the declaring resource.
func (s devSecretSources) missing(op, who, secret, key string) error {
	in := ""
	if s.envFile != "" {
		in = ", in " + s.envFile
	}
	return fault.Invalidf(op, "%s declares Secret %s with key %s; set it in the shell%s or under dev.env in %s", who, secret, key, in, devConfigFile)
}

// devFileState is funcdctl.config.yaml as dev last read it, for the reload (ADR-0254 Decision 6): its path, its last
// good keys, and the stamps of the file and of its envFile's contents.
type devFileState struct {
	path        string
	keys        devFileKeys
	envFilePath string // resolved; "" when the file names none
	fileStamp   string
	envStamp    string
	start       string          // the directory the file is in; its paths are relative to it
	appKeys     bool            // whether the App-only keys apply: dev.dir is the run's directory, or it is a template
	flagged     map[string]bool // the App flags given on the command line, which win over their keys at each reload
}

// loadDevFile reads funcdctl.config.yaml in dir, nil when there is none. It decodes it strictly (Decision 1): the
// root holds only dev, an unknown key is refused, platform takes only Decision 4's keys, and each duration parses.
func loadDevFile(op, dir string) (*devFileState, error) {
	path := filepath.Join(dir, devConfigFile)
	data, err := os.ReadFile(path) //nolint:gosec // the config file of the directory dev starts in
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Invalidf(op, "read %s: %v", path, err)
	}
	keys, err := decodeDevFile(op, data)
	if err != nil {
		return nil, err
	}
	st := &devFileState{path: path, keys: keys}
	st.fileStamp, _ = fileStamp(path)
	if keys.EnvFile != "" {
		st.envFilePath = filepath.Join(dir, keys.EnvFile)
		st.envStamp = envFileStamp(st.envFilePath)
	}
	return st, nil
}

// decodeDevFile decodes the file's bytes strictly into its dev keys.
func decodeDevFile(op string, data []byte) (devFileKeys, error) {
	var keys devFileKeys
	var root yamlv3.Node
	if err := yamlv3.NewDecoder(bytes.NewReader(data)).Decode(&root); errors.Is(err, io.EOF) {
		return keys, nil
	} else if err != nil {
		return keys, fault.Invalidf(op, "%s: %v", devConfigFile, err)
	}
	doc := root.Content[0]
	if doc.Kind != yamlv3.MappingNode {
		return keys, fault.Invalidf(op, "%s: the root is not a mapping", devConfigFile)
	}
	var dev *yamlv3.Node
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if k := doc.Content[i].Value; k != "dev" {
			return keys, fault.Invalidf(op, "%s: unknown key %s", devConfigFile, k)
		}
		dev = doc.Content[i+1]
	}
	if dev == nil || dev.Tag == "!!null" {
		return keys, nil
	}
	if dev.Kind != yamlv3.MappingNode {
		return keys, fault.Invalidf(op, "%s: dev is not a mapping", devConfigFile)
	}
	for i := 0; i+1 < len(dev.Content); i += 2 {
		k := dev.Content[i].Value
		if !slices.Contains(devFileKeyNames(), k) {
			return keys, fault.Invalidf(op, "%s: unknown key dev.%s", devConfigFile, k)
		}
		if k == "platform" {
			if err := checkPlatformKeys(op, dev.Content[i+1], ""); err != nil {
				return keys, err
			}
		}
		if k == "watch" {
			if err := checkWatchKeys(op, dev.Content[i+1]); err != nil {
				return keys, err
			}
		}
	}
	if err := dev.Decode(&keys); err != nil {
		return keys, fault.Invalidf(op, "%s: dev: %v", devConfigFile, err)
	}
	if err := checkWatchEntries(op, keys.Watch); err != nil {
		return keys, err
	}
	if _, err := keys.pace(op); err != nil {
		return keys, err
	}
	return keys, nil
}

// checkPlatformKeys refuses any leaf under platform that is not one of Decision 4's three keys.
func checkPlatformKeys(op string, n *yamlv3.Node, prefix string) error {
	refuse := func(key string) error {
		return fault.Invalidf(op, "%s: platform takes runtime.handOutSettle, eventing.blobPollInterval, funclog.segmentMaxAge and volumes.measureInterval, not %s",
			devConfigFile, key)
	}
	if n.Kind != yamlv3.MappingNode {
		return refuse(strings.TrimSuffix(prefix, "."))
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := prefix + n.Content[i].Value
		if slices.Contains(devPlatformKeys(), key) {
			continue
		}
		if !slices.ContainsFunc(devPlatformKeys(), func(k string) bool { return strings.HasPrefix(k, key+".") }) {
			return refuse(key)
		}
		if err := checkPlatformKeys(op, n.Content[i+1], key+"."); err != nil {
			return err
		}
	}
	return nil
}

// checkWatchKeys refuses a watch that is not a list and an entry key other than name, run and dir.
func checkWatchKeys(op string, n *yamlv3.Node) error {
	if n.Tag == "!!null" {
		return nil
	}
	if n.Kind != yamlv3.SequenceNode {
		return fault.Invalidf(op, "%s: dev.watch is not a list", devConfigFile)
	}
	for i, entry := range n.Content {
		if entry.Kind != yamlv3.MappingNode {
			return fault.Invalidf(op, "%s: dev.watch[%d] is not a mapping", devConfigFile, i)
		}
		for j := 0; j+1 < len(entry.Content); j += 2 {
			if k := entry.Content[j].Value; !slices.Contains([]string{"name", "run", "dir"}, k) {
				return fault.Invalidf(op, "%s: dev.watch[%d]: unknown key %s", devConfigFile, i, k)
			}
		}
	}
	return nil
}

// checkWatchEntries refuses a watch name that does not match watchNameFormat or is used twice, and an empty run.
func checkWatchEntries(op string, entries []devWatchEntry) error {
	seen := map[string]bool{}
	for i, e := range entries {
		switch {
		case !watchNameFormat.MatchString(e.Name):
			return fault.Invalidf(op, "%s: dev.watch[%d].name %s is not [a-z0-9][a-z0-9-]*", devConfigFile, i, e.Name)
		case seen[e.Name]:
			return fault.Invalidf(op, "%s: dev.watch[%d].name %s is used twice", devConfigFile, i, e.Name)
		case strings.TrimSpace(e.Run) == "":
			return fault.Invalidf(op, "%s: dev.watch[%d].run is empty", devConfigFile, i)
		}
		seen[e.Name] = true
	}
	return nil
}

// pace is the three cadences: each key the file sets, else Decision 4's default.
func (k devFileKeys) pace(op string) (devPace, error) {
	p := defaultDevPace()
	for _, d := range []struct {
		key string
		val string
		dst *time.Duration
	}{
		{"runtime.handOutSettle", k.Platform.Runtime.HandOutSettle, &p.handOutSettle},
		{"eventing.blobPollInterval", k.Platform.Eventing.BlobPollInterval, &p.blobPollInterval},
		{"funclog.segmentMaxAge", k.Platform.Funclog.SegmentMaxAge, &p.segmentMaxAge},
		{"volumes.measureInterval", k.Platform.Volumes.MeasureInterval, &p.volumeMeasure},
	} {
		if d.val == "" {
			continue
		}
		v, err := time.ParseDuration(d.val)
		if err != nil || v <= 0 {
			return p, fault.Invalidf(op, "%s: dev.platform.%s %q is not a positive duration", devConfigFile, d.key, d.val)
		}
		*d.dst = v
	}
	return p, nil
}

// readEnvFile reads an envFile (ADR-0254 Decision 5): KEY=value lines, # comments and blank lines, with one pair of
// double quotes around a value removed. A missing file reads as empty; any other line is malformed.
func readEnvFile(op, path string) (map[string]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the envFile the config file names
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fault.Invalidf(op, "read %s: %v", path, err)
	}
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || !envFileKeyFormat.MatchString(key) {
			return nil, fault.Invalidf(op, "%s:%d: not KEY=value", path, n)
		}
		if len(val) >= 2 && strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`) {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, fault.Invalidf(op, "read %s: %v", path, err)
	}
	return out, nil
}

// envFileStamp is the stamp of an envFile's contents, "" when it is missing.
func envFileStamp(path string) string {
	s, _ := fileStamp(path)
	return s
}

// secretSources reads the state's envFile and returns the sources a declared Secret takes its values from.
func (st *devFileState) secretSources(op string) (devSecretSources, error) {
	if st == nil {
		return devSecretSources{}, nil
	}
	src := devSecretSources{envFile: st.keys.EnvFile, env: st.keys.Env}
	if st.envFilePath != "" {
		vals, err := readEnvFile(op, st.envFilePath)
		if err != nil {
			return src, err
		}
		src.envFileVals = vals
	}
	return src, nil
}

// appOnlyKeysSet lists the App-only keys the file sets.
func (k devFileKeys) appOnlyKeysSet() []string {
	var out []string
	for _, c := range []struct {
		key string
		set bool
	}{{"values", len(k.Values) > 0}, {"build", len(k.Build) > 0}, {"hooks", k.Hooks}, {"apply", len(k.Apply) > 0},
		{"watch", len(k.Watch) > 0}, {"liveReload", k.LiveReload}} {
		if c.set {
			out = append(out, c.key)
		}
	}
	return out
}

// resolveDevRun reads funcdctl.config.yaml in start, the directory dev starts in, and merges it under the flags
// (ADR-0254 Decision 3): a flag given on the command line replaces its key, and a dir argument replaces dev.dir. It
// returns the directory to run, cfg filled, and the file's state (nil without a file).
func resolveDevRun(op, start string, args []string, flags *pflag.FlagSet, cfg *devConfig) (string, *devFileState, error) {
	cfg.pace = defaultDevPace()
	st, err := loadDevFile(op, start)
	if err != nil {
		return "", nil, err
	}
	dir := "."
	if st != nil && st.keys.Dir != "" {
		dir = st.keys.Dir
	}
	dir = filepath.Join(start, dir)
	argDir := len(args) == 1
	if argDir {
		dir = args[0]
	}
	if st == nil {
		return dir, nil, nil
	}
	k := st.keys
	if set := k.appOnlyKeysSet(); len(set) > 0 && !argDir && !isAppTemplate(dir) {
		return "", nil, fault.Invalidf(op, "%s: dev.%s needs dev.dir to be an App template (app.yaml beside resources/), and %s is not one",
			devConfigFile, set[0], dir)
	}
	changed := func(name string) bool { return flags != nil && flags.Changed(name) }
	st.start, st.appKeys = start, !argDir || isAppTemplate(dir)
	st.flagged = map[string]bool{}
	for _, f := range []string{"values", "build", "hooks", "apply"} {
		st.flagged[f] = changed(f)
	}
	st.mergeAppKeys(cfg)
	if st.appKeys {
		if cfg.watch, err = watchDirs(op, start, k.Watch); err != nil {
			return "", nil, err
		}
		cfg.liveReload = k.LiveReload
	}
	if !changed("persist") {
		cfg.persist = k.Persist
	}
	if !changed("persist-to") {
		// The default .funcd-dev is a path of the file too, so it lands beside the file wherever dev runs from.
		to := k.PersistTo
		if to == "" {
			to = devPersistDir
		}
		cfg.persistTo = filepath.Join(start, to)
	}
	for _, p := range []struct {
		flag string
		val  int
		dst  *int
	}{{"gport", k.Gport, &cfg.gport}, {"s3port", k.S3port, &cfg.s3port}, {"cport", k.Cport, &cfg.cport}} {
		if !changed(p.flag) {
			*p.dst = p.val
		}
	}
	if cfg.pace, err = k.pace(op); err != nil {
		return "", nil, err
	}
	if cfg.secrets, err = st.secretSources(op); err != nil {
		return "", nil, err
	}
	cfg.file = st
	return dir, st, nil
}

// watchDirs is entries with each dir made relative to start, the file's directory, which it defaults to; a dir that is
// not a directory is refused.
func watchDirs(op, start string, entries []devWatchEntry) ([]devWatchEntry, error) {
	out := make([]devWatchEntry, len(entries))
	for i, e := range entries {
		e.Dir = filepath.Join(start, e.Dir)
		if fi, err := os.Stat(e.Dir); err != nil || !fi.IsDir() {
			return nil, fault.Invalidf(op, "%s: dev.watch[%d].dir %s is not a directory", devConfigFile, i, e.Dir)
		}
		out[i] = e
	}
	return out, nil
}

// mergeAppKeys sets cfg's App-mode inputs from the file's values, build, hooks and apply, each unless its flag was
// given, with the paths made relative to the file's directory.
func (st *devFileState) mergeAppKeys(cfg *devConfig) {
	if !st.appKeys {
		return
	}
	k := st.keys
	rel := func(paths []string) []string {
		out := make([]string, 0, len(paths))
		for _, p := range paths {
			out = append(out, filepath.Join(st.start, p))
		}
		return out
	}
	if !st.flagged["values"] {
		cfg.values = rel(k.Values)
	}
	if !st.flagged["build"] {
		cfg.builds = make([]string, 0, len(k.Build))
		for _, b := range k.Build {
			if key, path, ok := strings.Cut(b, "="); ok {
				cfg.builds = append(cfg.builds, key+"="+filepath.Join(st.start, path))
				continue
			}
			cfg.builds = append(cfg.builds, filepath.Join(st.start, b))
		}
	}
	if !st.flagged["hooks"] {
		cfg.hooks = k.Hooks
	}
	if !st.flagged["apply"] {
		cfg.apply = rel(k.Apply)
	}
}

// reloadFile re-reads the file when it or its envFile's contents changed (ADR-0254 Decision 6). It reports whether
// the change reloads the folder, and updates the secret sources for it. A key that only a restart applies is printed
// and dev runs on as it was; a file that fails to decode is printed and the last good one stays.
func (st *devFileState) reloadFile(op string, src *devSecretSources, warn func(msg string, args ...any)) bool {
	if st == nil {
		return false
	}
	stamp, _ := fileStamp(st.path)
	envStamp := ""
	if st.envFilePath != "" {
		envStamp = envFileStamp(st.envFilePath)
	}
	envChanged := envStamp != st.envStamp
	if stamp == st.fileStamp && !envChanged {
		return false
	}
	st.fileStamp, st.envStamp = stamp, envStamp
	data, err := os.ReadFile(st.path)
	var keys devFileKeys
	if err == nil {
		keys, err = decodeDevFile(op, data)
	}
	if err != nil {
		warn("config file not applied", "err", err)
		return false
	}
	reload := envChanged
	for _, key := range changedDevKeys(st.keys, keys) {
		if slices.Contains(devReloadKeys(), key) {
			reload = true
			continue
		}
		warn(fmt.Sprintf("%s: dev.%s changed; restart funcdctl dev to apply it", devConfigFile, key))
	}
	st.keys = keys
	vals := src.envFileVals
	if st.envFilePath != "" {
		if vals, err = readEnvFile(op, st.envFilePath); err != nil {
			warn("config file not applied", "err", err)
			return false
		}
	}
	*src = devSecretSources{envFile: src.envFile, envFileVals: vals, env: keys.Env}
	return reload
}

// changedDevKeys names the dev keys whose values differ between a and b, platform's as platform.<key>.
func changedDevKeys(a, b devFileKeys) []string {
	var out []string
	for _, c := range []struct {
		key  string
		same bool
	}{
		{"dir", a.Dir == b.Dir}, {"values", slices.Equal(a.Values, b.Values)}, {"build", slices.Equal(a.Build, b.Build)},
		{"hooks", a.Hooks == b.Hooks}, {"apply", slices.Equal(a.Apply, b.Apply)}, {"persist", a.Persist == b.Persist},
		{"persistTo", a.PersistTo == b.PersistTo}, {"gport", a.Gport == b.Gport}, {"s3port", a.S3port == b.S3port},
		{"cport", a.Cport == b.Cport}, {"env", maps.Equal(a.Env, b.Env)}, {"envFile", a.EnvFile == b.EnvFile},
		{"platform.runtime.handOutSettle", a.Platform.Runtime.HandOutSettle == b.Platform.Runtime.HandOutSettle},
		{"platform.eventing.blobPollInterval", a.Platform.Eventing.BlobPollInterval == b.Platform.Eventing.BlobPollInterval},
		{"platform.funclog.segmentMaxAge", a.Platform.Funclog.SegmentMaxAge == b.Platform.Funclog.SegmentMaxAge},
		{"platform.volumes.measureInterval", a.Platform.Volumes.MeasureInterval == b.Platform.Volumes.MeasureInterval},
		{"watch", slices.Equal(a.Watch, b.Watch)}, {"liveReload", a.LiveReload == b.LiveReload},
	} {
		if !c.same {
			out = append(out, c.key)
		}
	}
	return out
}
