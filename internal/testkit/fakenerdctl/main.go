// Command fakenerdctl is the nerdctl the tests of internal/provider/nerdctl put on PATH (ADR-0334 Implementation plan):
// it keeps the containers and volumes it was told of in $FUNCD_FAKE_NERDCTL_DIR/state.json, appends each command and
// what it read on its standard input to calls.jsonl there, and answers as nerdctl does. One command at a time holds
// state.lock there while it reads, answers and writes, since a worker's log follower runs beside its inspect. An engine's published port is
// the one in the file port; a helper prints the next listing-<n> file for a find with stat, the file tar for a tar -c,
// and the file hosts, the VM's /etc/hosts, for a cat of it.
package main

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gofrs/flock"
)

// DirEnv names the directory of the fake's state.
const DirEnv = "FUNCD_FAKE_NERDCTL_DIR"

// state is what the fake knows.
type state struct {
	Containers map[string]container         `json:"containers"`
	Volumes    map[string]map[string]string `json:"volumes"`
	// Images are the references of the images a load brought in.
	Images []string `json:"images"`
}

type container struct {
	Running bool              `json:"running"`
	Labels  map[string]string `json:"labels"`
	Ports   map[string]string `json:"ports"`
	Volumes []string          `json:"volumes"`
}

// Call is one command the fake ran: its arguments and what it read on its standard input.
type Call struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

type inspected struct {
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]binding `json:"Ports"`
	} `json:"NetworkSettings"`
}

type binding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type volume struct {
	Name       string            `json:"Name"`
	Labels     map[string]string `json:"Labels"`
	Mountpoint string            `json:"Mountpoint"`
}

// mountpointOf is the directory the fake says holds volume name's files.
func mountpointOf(name string) string { return "/var/lib/nerdctl/volumes/" + name + "/_data" }

func main() {
	os.Exit(run(os.Getenv(DirEnv), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// fake is one command's view of the state and its arguments.
type fake struct {
	dir    string
	args   []string
	st     state
	stdout io.Writer
	stderr io.Writer
	in     string // what the command read on its standard input
}

func run(dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if dir == "" || len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "fakenerdctl: "+DirEnv+" and a command are required")
		return 2
	}
	lock := flock.New(filepath.Join(dir, "state.lock"))
	if err := lock.Lock(); err != nil {
		_, _ = fmt.Fprintln(stderr, "fakenerdctl:", err)
		return 2
	}
	defer func() { _ = lock.Unlock() }()
	f := &fake{dir: dir, args: args, stdout: stdout, stderr: stderr,
		st: state{Containers: map[string]container{}, Volumes: map[string]map[string]string{}}}
	if b, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		_ = json.Unmarshal(b, &f.st)
	}
	in := ""
	if slices.Contains(args, "-i") || slices.Contains(args, "/dev/stdin") || args[0] == "load" {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	if err := appendCall(dir, Call{Args: args, Stdin: in}); err != nil {
		_, _ = fmt.Fprintln(stderr, "fakenerdctl:", err)
		return 2
	}
	f.in = in
	code := f.answer()
	b, _ := json.Marshal(f.st)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0o600); err != nil {
		_, _ = fmt.Fprintln(stderr, "fakenerdctl:", err)
		return 2
	}
	return code
}

func appendCall(dir string, c Call) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	return firstErr(werr, f.Close())
}

func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// fail writes nerdctl's error form of msg and returns its exit status.
func (f *fake) fail(msg string) int {
	_, _ = fmt.Fprintf(f.stderr, "level=fatal msg=%q\n", msg)
	return 1
}

// values are the values of each flag in the arguments, in order.
func (f *fake) values(flag string) []string {
	var out []string
	for i := 0; i+1 < len(f.args); i++ {
		if f.args[i] == flag {
			out = append(out, f.args[i+1])
		}
	}
	return out
}

func (f *fake) labels() map[string]string {
	out := map[string]string{}
	for _, l := range f.values("--label") {
		k, v, _ := strings.Cut(l, "=")
		out[k] = v
	}
	return out
}

// matches reports whether labels hold each --filter label=k=v.
func (f *fake) matches(labels map[string]string) bool {
	for _, flt := range f.values("--filter") {
		k, v, _ := strings.Cut(strings.TrimPrefix(flt, "label="), "=")
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (f *fake) answer() int {
	a, last := f.args, f.args[len(f.args)-1]
	switch {
	case a[0] == "version":
		_, _ = fmt.Fprintln(f.stdout, "linux/arm64")
	case a[0] == "ps":
		for _, n := range slices.Sorted(maps.Keys(f.st.Containers)) {
			if f.matches(f.st.Containers[n].Labels) {
				_, _ = fmt.Fprintln(f.stdout, n)
			}
		}
	case len(a) > 1 && a[0] == "container" && a[1] == "inspect":
		return f.inspect(last)
	case a[0] == "run" && slices.Contains(a, "-d"):
		f.runEngine()
	case a[0] == "run":
		f.helper()
	case a[0] == "stop":
		c, ok := f.st.Containers[last]
		if !ok {
			return f.fail("1 errors:\nno such container: " + last)
		}
		c.Running = false
		f.st.Containers[last] = c
	case a[0] == "rm":
		if _, ok := f.st.Containers[last]; !ok {
			return f.fail("1 errors:\nno such container: " + last)
		}
		delete(f.st.Containers, last)
	case len(a) > 1 && a[0] == "volume":
		return f.volume(a[1], last)
	case len(a) > 2 && a[0] == "image" && a[1] == "inspect":
		if !slices.Contains(f.st.Images, last) {
			return f.fail("1 errors:\nno such image: " + last)
		}
		_, _ = fmt.Fprintln(f.stdout, "[{}]")
	case a[0] == "load":
		return f.load()
	default:
		return f.fail("unknown command " + strings.Join(a, " "))
	}
	return 0
}

// load reads the image tar on its standard input, as docker save writes it, and keeps the references its manifest.json
// names.
func (f *fake) load() int {
	tr := tar.NewReader(strings.NewReader(f.in))
	for {
		h, err := tr.Next()
		if err != nil {
			return f.fail("unrecognized image format")
		}
		if h.Name != "manifest.json" {
			continue
		}
		var manifest []struct{ RepoTags []string }
		if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
			return f.fail("unrecognized image format")
		}
		for _, m := range manifest {
			for _, ref := range m.RepoTags {
				if !slices.Contains(f.st.Images, ref) {
					f.st.Images = append(f.st.Images, ref)
				}
			}
		}
		return 0
	}
}

func (f *fake) inspect(name string) int {
	c, ok := f.st.Containers[name]
	if !ok {
		return f.fail("1 errors:\nno such container: " + name)
	}
	var in inspected
	in.State.Running, in.Config.Labels = c.Running, c.Labels
	in.NetworkSettings.Ports = map[string][]binding{}
	for p, addr := range c.Ports {
		host, port, _ := net.SplitHostPort(addr)
		in.NetworkSettings.Ports[p] = []binding{{HostIP: host, HostPort: port}}
	}
	b, _ := json.Marshal([]inspected{in})
	_, _ = f.stdout.Write(b)
	return 0
}

// runEngine starts an engine: each -p container port is published at the port of the file port.
func (f *fake) runEngine() {
	port, _ := os.ReadFile(filepath.Join(f.dir, "port"))
	c := container{Running: true, Labels: f.labels(), Ports: map[string]string{}}
	for _, p := range f.values("-p") {
		c.Ports[p[strings.LastIndex(p, ":")+1:]+"/tcp"] = net.JoinHostPort("127.0.0.1", strings.TrimSpace(string(port)))
	}
	for _, m := range f.values("--mount") {
		// A bind mount holds no volume in use, as real nerdctl sees one, even of a volume's own directory.
		if !strings.Contains(m, "type=volume") {
			continue
		}
		for _, part := range strings.Split(m, ",") {
			if v, ok := strings.CutPrefix(part, "source="); ok {
				c.Volumes = append(c.Volumes, v)
			}
		}
	}
	f.st.Containers[f.values("--name")[0]] = c
}

// helper answers a helper's run: the next listing-<n> file for a find with stat, the file tar for a tar -c, the file hosts
// for a cat of /etc/hosts.
func (f *fake) helper() {
	line := strings.Join(f.args, " ")
	switch {
	case strings.Contains(line, "find") && strings.Contains(line, "stat"):
		n := 0
		for {
			if _, err := os.Stat(filepath.Join(f.dir, fmt.Sprintf("listing-%d.done", n))); err != nil {
				break
			}
			n++
		}
		b, _ := os.ReadFile(filepath.Join(f.dir, fmt.Sprintf("listing-%d", n)))
		_ = os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("listing-%d.done", n)), nil, 0o600)
		_, _ = f.stdout.Write(b)
	case slices.Contains(f.args, "tar") && slices.Contains(f.args, "-c"):
		b, _ := os.ReadFile(filepath.Join(f.dir, "tar"))
		_, _ = f.stdout.Write(b)
	case strings.HasSuffix(line, " cat /etc/hosts"):
		b, _ := os.ReadFile(filepath.Join(f.dir, "hosts"))
		_, _ = f.stdout.Write(b)
	}
}

func (f *fake) volume(verb, last string) int {
	switch verb {
	case "create":
		f.st.Volumes[last] = f.labels()
	case "ls":
		for _, n := range slices.Sorted(maps.Keys(f.st.Volumes)) {
			if f.matches(f.st.Volumes[n]) {
				_, _ = fmt.Fprintln(f.stdout, n)
			}
		}
	case "inspect":
		var vols []volume
		for _, n := range f.args[2:] {
			vols = append(vols, volume{Name: n, Labels: f.st.Volumes[n], Mountpoint: mountpointOf(n)})
		}
		b, _ := json.Marshal(vols)
		_, _ = f.stdout.Write(b)
	case "rm":
		if _, ok := f.st.Volumes[last]; !ok {
			return f.fail(`volume "` + last + `": not found`)
		}
		for _, c := range f.st.Containers {
			if slices.Contains(c.Volumes, last) {
				return f.fail(`volume "` + last + `" is in use (failed precondition)`)
			}
		}
		delete(f.st.Volumes, last)
	default:
		return f.fail("unknown volume command " + verb)
	}
	return 0
}
