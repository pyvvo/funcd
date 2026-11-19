// Package nerdctl runs the add-on engines of funcdctl dev on containerd through the nerdctl CLI (ADR-0334 Decision 1):
// the Runtime of internal/provider/docker over an Engine that runs nerdctl. It reaches every volume only through the
// helper container, nerdctl run --rm -i -v <volume>:/d <helper> (Decision 4), since nerdctl cp needs a running
// container and lacks --archive.
package nerdctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"os/exec"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/provider/docker"
)

// maxOutputBytes bounds what a nerdctl command's output is read into: an inspect, a listing, its error.
const maxOutputBytes = 8 << 20

// Runtime is the nerdctl driver: internal/provider/docker's add-on runtime over the nerdctl CLI.
type Runtime struct{ *docker.Runtime }

// Find returns nerdctl on PATH; Unavailable when it is not there, naming colima nerdctl install on macOS.
func Find() (string, error) {
	bin, err := exec.LookPath("nerdctl")
	if err == nil {
		return bin, nil
	}
	msg := "nerdctl is not on PATH"
	if runtime.GOOS == "darwin" {
		msg += ": with colima's containerd runtime (colima start --runtime containerd), run colima nerdctl install"
	}
	return "", fault.Unavailablef("nerdctl.Find", "%s", msg)
}

// New is the nerdctl driver of bin for the session key. As docker.New, it reaps the key's leftover containers, helpers
// included, and, unless persist, its volumes before it returns (Decision 7).
func New(ctx context.Context, bin, key string, persist bool, logger *slog.Logger) (*Runtime, error) {
	rt, err := docker.NewRuntime(ctx, cli{bin: bin}, key, persist, logger)
	if err != nil {
		return nil, err
	}
	return &Runtime{Runtime: rt}, nil
}

// cli is the Engine of the nerdctl CLI. nerdctl pulls with the developer's own login (Decision 2).
type cli struct{ bin string }

var _ docker.Engine = cli{}

// run runs nerdctl with args, stdin on its standard input, and returns its standard output; a failure is an error of
// the kind its standard error names.
func (c cli) run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.bin, args...) //nolint:gosec // nerdctl, found on PATH, with the driver's own arguments
	cmd.Stdin = stdin
	var out, errOut limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, cliError(ctx, args, errOut.String(), err)
	}
	return out.Bytes(), nil
}

// cliError is a failed command's error: NotFound when nerdctl names no such object, Conflict when one is in use,
// Unavailable when the command did not end on its own, else Internal; it holds what nerdctl wrote on its standard error.
func cliError(ctx context.Context, args []string, stderr string, err error) error {
	const op = "nerdctl"
	call := "nerdctl " + strings.Join(args[:min(len(args), 2)], " ")
	msg := lastWords(stderr)
	switch low := strings.ToLower(stderr); {
	case ctx.Err() != nil:
		return fault.Wrapf(err, fault.Unavailable, op, "%s did not end: %s", call, msg)
	case strings.Contains(low, "no such container"), strings.Contains(low, "no such object"), strings.Contains(low, "not found"):
		return fault.Wrapf(err, fault.NotFound, op, "%s: %s", call, msg)
	case strings.Contains(low, "is in use"):
		return fault.Wrapf(err, fault.Conflict, op, "%s: %s", call, msg)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return fault.Wrapf(err, fault.Unavailable, op, "run %s", call)
	}
	return fault.Wrapf(err, fault.Internal, op, "%s: %s", call, msg)
}

// lastWords is what a failed nerdctl said: the messages of its fatal and error lines, else its last line, so a pull's
// progress before them is left out.
func lastWords(stderr string) string {
	var msgs []string
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.Contains(line, "level=fatal") && !strings.Contains(line, "level=error") {
			continue
		}
		_, rest, ok := strings.Cut(line, "msg=")
		switch q, err := strconv.QuotedPrefix(rest); {
		case !ok:
			rest = line
		case err == nil:
			rest, _ = strconv.Unquote(q)
		}
		msgs = append(msgs, strings.TrimSpace(rest))
	}
	if len(msgs) > 0 {
		return strings.Join(msgs, "; ")
	}
	all := lines([]byte(stderr))
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

// labelArgs are labels as repeated flag pairs, in order.
func labelArgs(flag string, labels map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		out = append(out, flag, k+"="+labels[k])
	}
	return out
}

func filterArgs(labels map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		out = append(out, "--filter", "label="+k+"="+labels[k])
	}
	return out
}

func lines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (c cli) Containers(ctx context.Context, labels map[string]string) ([]string, error) {
	out, err := c.run(ctx, nil, append([]string{"ps", "-a", "--format", "{{.Names}}"}, filterArgs(labels)...)...)
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

func (c cli) Inspect(ctx context.Context, name string) (docker.Container, bool, error) {
	out, err := c.run(ctx, nil, "container", "inspect", name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return docker.Container{}, false, nil
	case err != nil:
		return docker.Container{}, false, err
	}
	var in []struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(out, &in); err != nil || len(in) == 0 {
		return docker.Container{}, false, fault.Internalf("nerdctl", "decode the container %s: %v", name, err)
	}
	ct := docker.Container{Running: in[0].State.Running, Labels: in[0].Config.Labels, Published: map[string]string{}}
	for port, binds := range in[0].NetworkSettings.Ports {
		for _, b := range binds {
			if b.HostPort != "" && (b.HostIP == "" || b.HostIP == "127.0.0.1") {
				ct.Published[port] = "127.0.0.1:" + b.HostPort
				break
			}
		}
	}
	return ct, true, nil
}

// Pull leaves the pull to the engine's own start, nerdctl run --pull missing, which uses the developer's login.
func (cli) Pull(context.Context, string) error { return nil }

func (c cli) Run(ctx context.Context, spec docker.RunSpec) error {
	args := []string{"run", "-d", "--name", spec.Name, "--pull", "missing", "--restart", "no", "--stop-signal", "SIGTERM",
		"--stop-timeout", strconv.Itoa(int(spec.StopGrace / time.Second)), "-p", "127.0.0.1::" + strconv.Itoa(spec.Port)}
	args = append(args, labelArgs("--label", spec.Labels)...)
	if spec.Volume != "" {
		// nerdctl's releases take no volume-nocopy, so the engine mounts the volume's own directory as a bind, which
		// never copies the image's files up (ADR-0334 Decision 3).
		dir, err := c.mountpoint(ctx, spec.Volume)
		if err != nil {
			return err
		}
		args = append(args, "--mount", "type=bind,source="+dir+",target="+spec.Target)
	}
	// An env file on nerdctl's standard input keeps the values, Secrets among them, off its command line; an env file
	// holds no newline, so a value with one goes on it.
	var envFile bytes.Buffer
	for _, k := range slices.Sorted(maps.Keys(spec.Env)) {
		if v := spec.Env[k]; strings.ContainsAny(v, "\n\r") {
			args = append(args, "-e", k+"="+v)
		} else {
			envFile.WriteString(k + "=" + v + "\n")
		}
	}
	var stdin io.Reader
	if envFile.Len() > 0 {
		args, stdin = append(args, "--env-file", "/dev/stdin"), &envFile
	}
	_, err := c.run(ctx, stdin, append(args, spec.Image)...)
	return err
}

func (c cli) Stop(ctx context.Context, name string, grace time.Duration) error {
	_, err := c.run(ctx, nil, "stop", "-t", strconv.Itoa(int(grace/time.Second)), name)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	return err
}

func (c cli) Remove(ctx context.Context, name string) error {
	_, err := c.run(ctx, nil, "rm", "-f", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	return err
}

// mountpoint is the directory that holds volume name's files, where nerdctl runs.
func (c cli) mountpoint(ctx context.Context, name string) (string, error) {
	out, err := c.run(ctx, nil, "volume", "inspect", name)
	if err != nil {
		return "", err
	}
	var vols []struct {
		Mountpoint string `json:"Mountpoint"`
	}
	if err := json.Unmarshal(out, &vols); err != nil || len(vols) != 1 || vols[0].Mountpoint == "" {
		return "", fault.Internalf("nerdctl", "read the directory of the volume %s: %v", name, err)
	}
	return vols[0].Mountpoint, nil
}

func (c cli) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	_, err := c.run(ctx, nil, append(append([]string{"volume", "create"}, labelArgs("--label", labels)...), name)...)
	return err
}

func (c cli) Volumes(ctx context.Context, labels map[string]string) ([]docker.Volume, error) {
	out, err := c.run(ctx, nil, append([]string{"volume", "ls", "-q"}, filterArgs(labels)...)...)
	if err != nil {
		return nil, err
	}
	names := lines(out)
	if len(names) == 0 {
		return nil, nil
	}
	out, err = c.run(ctx, nil, append([]string{"volume", "inspect"}, names...)...)
	if err != nil {
		return nil, err
	}
	var vols []docker.Volume
	if err := json.Unmarshal(out, &vols); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "nerdctl", "decode the volumes %s", strings.Join(names, " "))
	}
	return vols, nil
}

func (c cli) RemoveVolume(ctx context.Context, name string) error {
	_, err := c.run(ctx, nil, "volume", "rm", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	return err
}

// helperArgs runs h with --rm: its name, labels and volumes, then the image.
func helperArgs(h docker.Helper, interactive bool) []string {
	args := []string{"run", "--rm", "--name", h.Name}
	if interactive {
		args = append(args, "-i")
	}
	args = append(args, labelArgs("--label", h.Labels)...)
	for _, m := range h.Mounts {
		args = append(args, "-v", m.Volume+":"+m.Target)
	}
	return append(args, h.Image)
}

func (c cli) Exec(ctx context.Context, h docker.Helper, argv ...string) ([]byte, error) {
	return c.run(ctx, nil, append(helperArgs(h, false), argv...)...)
}

// ReadTar streams the helper's tar of p; closing it waits for the helper's end.
func (c cli) ReadTar(ctx context.Context, h docker.Helper, p string) (io.ReadCloser, error) {
	args := append(helperArgs(h, false), "tar", "-c", "-f", "-", "-C", path.Dir(p), path.Base(p))
	cmd := exec.CommandContext(ctx, c.bin, args...) //nolint:gosec // nerdctl, found on PATH, with the driver's own arguments
	var errOut limitedBuffer
	cmd.Stderr = &errOut
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "nerdctl", "read %s", p)
	}
	if err := cmd.Start(); err != nil {
		return nil, cliError(ctx, args, "", err)
	}
	return stream{ReadCloser: out, wait: func() error {
		if err := cmd.Wait(); err != nil {
			return cliError(ctx, args, errOut.String(), err)
		}
		return nil
	}}, nil
}

// stream is a helper's standard output; Close ends the read and waits for the helper.
type stream struct {
	io.ReadCloser
	wait func() error
}

func (s stream) Close() error {
	_ = s.ReadCloser.Close()
	return s.wait()
}

// writeScript makes each missing directory of $1, owned by $2 at mode 0700, then extracts the tar on standard input
// into it, as root, so each entry keeps its owner.
const writeScript = `set -e
p=
IFS=/
for s in $1; do
  [ -n "$s" ] || continue
  p="$p/$s"
  if [ ! -d "$p" ]; then mkdir "$p"; chown "$2" "$p"; chmod 700 "$p"; fi
done
unset IFS
tar -x -f - -C "$1"`

func (c cli) WriteTar(ctx context.Context, h docker.Helper, dir string, uid, gid int, archive io.Reader) error {
	_, err := c.run(ctx, archive, append(helperArgs(h, true), "sh", "-c", writeScript, "sh", dir,
		strconv.Itoa(uid)+":"+strconv.Itoa(gid))...)
	return err
}

// limitedBuffer keeps the first maxOutputBytes written to it and drops the rest.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxOutputBytes - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
