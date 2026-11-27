package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-isatty"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	appctl "github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// boxPalette is the palette of funcdctl's boxes: the dev banner's and the App view's (ADR-0362 Decision 2).
type boxPalette struct{ accent, link, ok, dim, warn, bad lipgloss.AdaptiveColor }

func palette() boxPalette {
	return boxPalette{
		accent: lipgloss.AdaptiveColor{Light: "#7D56F4", Dark: "#B694FF"},
		link:   lipgloss.AdaptiveColor{Light: "#0969DA", Dark: "#58A6FF"},
		ok:     lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#3FB950"},
		dim:    lipgloss.AdaptiveColor{Light: "#57606A", Dark: "#8B949E"},
		warn:   lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#E3B341"},
		bad:    lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#F85149"},
	}
}

// liveTerminal reports whether w is a terminal the App view may redraw (ADR-0362 Decision 1).
func liveTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && liveFile(os.Getenv("TERM"), isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}

// liveFile is a terminal other than TERM=dumb; every other output gets lines.
func liveFile(term string, tty bool) bool { return tty && term != "dumb" }

// appView draws a deploy's or a delete's progress: a live box on a terminal, lines elsewhere (ADR-0362 Decisions 1
// and 6). Times run on now, from the first poll that sees a row unsettled.
type appView struct {
	out    io.Writer
	live   bool
	now    func() time.Time
	width  func() int
	height func() int
	r      *lipgloss.Renderer
	start  time.Time
	drawn  int
	box    string
	since  map[string]time.Time
	took   map[string]time.Duration
	plan   *v1.AppPlan
	calls  map[v1.ObjectName]*v1.Invocation
	last   func(fold bool) []string // the last box's builder, which done draws unfolded
	folded bool
}

func newAppView(out io.Writer, live bool, now func() time.Time) *appView {
	size := func() (int, int) {
		if f, ok := out.(*os.File); ok {
			if w, h, err := term.GetSize(f.Fd()); err == nil && w > 20 && h > 5 {
				return w, h
			}
		}
		return 120, 1000
	}
	return &appView{out: out, live: live, now: now, r: lipgloss.NewRenderer(out), start: now(),
		since: map[string]time.Time{}, took: map[string]time.Duration{}, calls: map[v1.ObjectName]*v1.Invocation{},
		width:  func() int { w, _ := size(); return w },
		height: func() int { _, h := size(); return h },
	}
}

// clock times the row id: it starts at the first poll that sees it unsettled and stops at the first that sees it
// done, so a row settled at its first poll took 0s. It returns the time to show, with … while it runs.
func (v *appView) clock(id string, done bool) string {
	if d, ok := v.took[id]; ok {
		return span(d)
	}
	t, ok := v.since[id]
	switch {
	case !ok && done:
		v.took[id] = 0
		return span(0)
	case !ok:
		v.since[id], t = v.now(), v.now()
	case done:
		v.took[id] = v.now().Sub(t)
		return span(v.took[id])
	}
	return span(v.now().Sub(t)) + "…"
}

// span is a duration to the second: 12s, 2m10s.
func span(d time.Duration) string { return d.Round(time.Second).String() }

// poll reads what one poll of a deploy shows beside the App and its followed revision, then draws it: the required
// Apps and the recorded hook calls' Invocations, each Invocation read once.
func (v *appView) poll(ctx context.Context, c *sdk.Client, a *v1.App, rev *v1.AppRevision) error {
	required, err := requiredApps(ctx, c, a)
	if err != nil {
		return err
	}
	if rev != nil {
		for _, h := range rev.Status.Hooks {
			if _, ok := v.calls[h.Invocation]; ok || h.Invocation == "" {
				continue
			}
			obj, err := c.Get(ctx, v1.KindInvocation, a.Namespace, h.Invocation)
			switch {
			case fault.KindOf(err) == fault.NotFound:
				v.calls[h.Invocation] = nil
			case err != nil:
				return err
			default:
				v.calls[h.Invocation], _ = obj.(*v1.Invocation)
			}
		}
	}
	return v.deploy(a, rev, actions(v.plan), required, appParts(a), v.calls)
}

// appParts are the parts a's spec declares, by kind and name, an add-on part under AddonObject.
func appParts(a *v1.App) map[v1.ObjectRef]v1.Object {
	parts := map[v1.ObjectRef]v1.Object{}
	for _, p := range a.Parts() {
		parts[v1.ObjectRef{Kind: p.GroupVersionKind().Kind, Name: p.GetName()}] = p
	}
	return parts
}

// requiredApps gets each App that a's spec.requires names, nil for one that does not exist.
func requiredApps(ctx context.Context, c *sdk.Client, a *v1.App) (map[v1.ObjectName]*v1.App, error) {
	out := map[v1.ObjectName]*v1.App{}
	for _, e := range a.Spec.Requires {
		dep, err := storedApp(ctx, c, a.Namespace, e.App)
		if err != nil {
			return nil, err
		}
		out[e.App] = dep
	}
	return out, nil
}

// actions is a plan's action per part, nil for no plan or for one that holds a stop, a line with a reason
// (ADR-0362 Decision 2): ChildNotOwned, a namesake or a Secret's stop (ADR-0220 Decision 5).
func actions(plan *v1.AppPlan) map[v1.ObjectRef]v1.PlanAction {
	if plan == nil || slices.ContainsFunc(plan.Parts, func(p v1.PlanPart) bool { return p.Action == "" || p.Reason != "" }) {
		return nil
	}
	out := map[v1.ObjectRef]v1.PlanAction{}
	for _, p := range plan.Parts {
		out[v1.ObjectRef{Kind: p.Kind, Name: p.Name}] = p.Action
	}
	return out
}

// deploy draws one poll of a deploy: the App, the followed revision (nil before its stamp), the plan's action per
// part, the required Apps by name, the App's parts by reference and the hooks' Invocations. While the revision waits
// for its required Apps, before its clock starts, no part row is drawn (ADR-0219 Decision 3).
func (v *appView) deploy(a *v1.App, rev *v1.AppRevision, plan map[v1.ObjectRef]v1.PlanAction,
	required map[v1.ObjectName]*v1.App, parts map[v1.ObjectRef]v1.Object, hooks map[v1.ObjectName]*v1.Invocation) error {
	st := v.styles()
	title := st.title.Render("● funcd app deploy") + st.dim.Render(fmt.Sprintf("   %s %s", a.Name, termSafe(a.Spec.Version)))
	if rev != nil {
		title += st.dim.Render(" · revision " + string(rev.Name))
	}
	head := []string{title + st.dim.Render(" · Ctrl-C stops the wait")}
	if plan != nil {
		head = append(head, st.dim.Render("plan  ")+planCounts(plan, v.plan))
	}
	reqRows, unmet := v.requireRows(a, required, st)
	waiting := unmet > 0 && (rev == nil || rev.Status.StartedAt == nil)
	if waiting {
		head = append(head, st.warn.Render(fmt.Sprintf("waiting for %d of %d required Apps", unmet, len(a.Spec.Requires)))+
			st.dim.Render(" · no deadline yet · "+span(v.now().Sub(v.start))))
	} else {
		head = append(head, v.progress(a, rev, st))
	}
	build := func(fold bool) []string {
		lines := slices.Clone(head)
		if len(reqRows) > 0 {
			lines = append(lines, "", st.title.Render("REQUIRES"), table(reqRows))
		}
		if rows := v.hookRows(rev, hooks, st); len(rows) > 0 {
			lines = append(lines, "", st.title.Render("HOOKS"), table(rows))
		}
		lines = append(lines, "", st.title.Render("PARTS"))
		if waiting {
			lines = append(lines, st.dim.Render(fmt.Sprintf("⋯ %d parts wait for the required Apps; none is written and no hook runs", len(parts))))
		} else {
			lines = append(lines, table(v.partRows(a, plan, parts, st, fold)))
		}
		if r := readyLine(a); r != "" {
			lines = append(lines, "", st.dim.Render("App/"+string(a.Name)+" "+termSafe(r)))
		}
		return lines
	}
	return v.drawFitting(build, st)
}

// planCounts is the plan line: its create, update and prune parts, and its hooks.
func planCounts(acts map[v1.ObjectRef]v1.PlanAction, plan *v1.AppPlan) string {
	count := map[v1.PlanAction]int{}
	for _, act := range acts {
		count[act]++
	}
	hooks := 0
	if plan != nil {
		hooks = len(plan.Hooks)
	}
	return fmt.Sprintf("%d create · %d update · %d prune · %d hooks", count[v1.PlanCreate], count[v1.PlanUpdate], count[v1.PlanPrune], hooks)
}

// settled is a part the App counts as such for its Ready, its switch and its deadline (ADR-0357 Decision 1).
func settled(c v1.AppChild) bool {
	return c.State == v1.AppChildReady || c.State == v1.AppChildNotStarted || c.State == v1.AppChildLagging
}

// progress is the progress line: ▰ per settled part, the time since startedAt and the deadline, or the App's Ready
// reason once the deadline has passed.
func (v *appView) progress(a *v1.App, rev *v1.AppRevision, st viewStyles) string {
	children := slices.DeleteFunc(slices.Clone(a.Status.Children), func(c v1.AppChild) bool { return c.State == v1.AppChildPruning })
	n := 0
	for _, c := range children {
		if settled(c) {
			n++
		}
	}
	bar := st.ok.Render(strings.Repeat("▰", n)) + st.muted.Render(strings.Repeat("▱", len(children)-n))
	line := fmt.Sprintf("%s  %d/%d ready", bar, n, len(children))
	if rev == nil || rev.Status.StartedAt == nil {
		return line
	}
	now := v.now()
	line += st.dim.Render(" · " + span(now.Sub(time.Time(*rev.Status.StartedAt))))
	switch d := rev.Status.Deadline; {
	case rev.Name == a.Status.CurrentRevision || rev.Status.Phase != v1.PhaseDeploying || d == nil:
	case now.Before(time.Time(*d)):
		line += st.dim.Render(" · deadline in " + span(time.Time(*d).Sub(now)))
	default:
		c, _ := a.Status.Conditions.Get("Ready")
		line += st.warn.Render(" · " + termSafe(cmp.Or(c.Reason, "past the deadline")))
	}
	return line
}

// requireRows are the REQUIRES rows and how many requirements are unmet (ADR-0362 Decision 2).
func (v *appView) requireRows(a *v1.App, required map[v1.ObjectName]*v1.App, st viewStyles) ([][]string, int) {
	var rows [][]string
	unmet := 0
	for _, e := range a.Spec.Requires {
		dep := required[e.App]
		why := appctl.Unmet(e, dep)
		version := "–"
		if dep != nil {
			version = termSafe(dep.Spec.Version)
		}
		icon, state := st.ok.Render("✓"), st.ok.Render("Ready")
		switch {
		case why == "":
		case dep == nil || strings.HasPrefix(why, "is ") && !strings.HasPrefix(why, "is not Ready"), strings.HasPrefix(why, "has no SemVer"):
			icon, state = st.bad.Render("✗"), st.bad.Render(termSafe(why))
		default:
			icon, state = st.warn.Render("◐"), st.warn.Render(termSafe(why))
		}
		if why != "" {
			unmet++
		}
		rows = append(rows, []string{icon + " App/" + string(e.App), version, st.dim.Render(termSafe(cmp.Or(e.Version, "any"))), state,
			v.clock("requires/"+string(e.App), why == "")})
	}
	return rows, unmet
}

// hookRows are the HOOKS rows: each hook of the followed revision, due until status.hooks records its call, then
// done or failed with its Invocation's time (ADR-0362 Decision 2).
func (v *appView) hookRows(rev *v1.AppRevision, calls map[v1.ObjectName]*v1.Invocation, st viewStyles) [][]string {
	if rev == nil || rev.Spec.Spec.Hooks == nil {
		return nil
	}
	var rows [][]string
	add := func(point string, hooks []v1.AppHook) {
		for _, h := range hooks {
			id := point + "/" + string(h.Function)
			label := point + " Function/" + string(h.Function)
			call, recorded := lastCall(rev, point, h.Function)
			if !recorded {
				rows = append(rows, []string{st.warn.Render("◐") + " " + label, st.warn.Render("due"), v.clock(id, false)})
				continue
			}
			took := "–"
			if inv := calls[call.Invocation]; inv != nil && !inv.Status.StartTime.IsZero() {
				took = span(time.Time(inv.Status.EndTime).Sub(time.Time(inv.Status.StartTime)))
			}
			icon, state := st.ok.Render("✓"), st.ok.Render("done")
			if call.Phase == v1.PhaseFailed {
				icon, state = st.bad.Render("✗"), st.bad.Render("failed")
			}
			rows = append(rows, []string{icon + " " + label, state, took})
		}
	}
	add("preApply", rev.Spec.Spec.Hooks.PreApply)
	add("postApply", rev.Spec.Spec.Hooks.PostApply)
	return rows
}

// lastCall is the last recorded call of a hook, a retried one included.
func lastCall(rev *v1.AppRevision, point string, fn v1.ObjectName) (v1.AppHookCall, bool) {
	for i := len(rev.Status.Hooks) - 1; i >= 0; i-- {
		if c := rev.Status.Hooks[i]; c.Point == point && c.Function == fn {
			return c, true
		}
	}
	return v1.AppHookCall{}, false
}

// partRows are the PARTS rows, one per status.children entry: its icon, action, state, what it waits on and its time
// (ADR-0362 Decisions 2, 4 and 5). fold folds the Ready and NotStarted rows into one, for a box the terminal is too
// short to hold.
func (v *appView) partRows(a *v1.App, plan map[v1.ObjectRef]v1.PlanAction, parts map[v1.ObjectRef]v1.Object, st viewStyles, fold bool) [][]string {
	var rows [][]string
	folded := 0
	for _, c := range a.Status.Children {
		id := string(c.Kind) + "/" + string(c.Name)
		ref := v1.ObjectRef{Kind: c.Kind, Name: c.Name}
		act := plan[ref]
		done := c.State == v1.AppChildReady || c.State == v1.AppChildNotStarted
		state := strings.TrimSpace(string(c.State) + " " + st.dim.Render(termSafe(c.Reason)))
		var icon, waits, took string
		switch {
		case c.State == v1.AppChildPruning:
			icon, act, took = st.dim.Render("⋯"), v1.PlanPrune, "–"
		case done:
			icon, state = st.ok.Render("✓"), st.ok.Render(string(c.State))
		case c.Reason == "ChildInvalid" || c.Reason == "ChildNotOwned":
			icon = st.bad.Render("✗")
		default:
			icon = st.warn.Render("◐")
			waits = waitsOn(a, parts[partKey(a, c)])
		}
		if took == "" {
			if _, planned := plan[ref]; plan != nil && !planned && done && v.since[id].IsZero() {
				took = "–"
			} else {
				took = v.clock(id, done)
			}
		}
		if fold && done {
			folded++
			continue
		}
		rows = append(rows, []string{icon + " " + id, string(act), state, st.link.Render(waits), took})
	}
	if folded > 0 {
		rows = append(rows, []string{st.ok.Render("✓") + fmt.Sprintf(" %d more ready", folded), "", "", "", ""})
	}
	return rows
}

// partKey is the reference parts holds a child under: an add-on part by its stored kind.
func partKey(a *v1.App, c v1.AppChild) v1.ObjectRef {
	if slices.ContainsFunc(a.Spec.Addons, func(e v1.AppAddon) bool { return e.Name == c.Name && v1.SameAddonKind(e.Kind, string(c.Kind)) }) {
		return v1.ObjectRef{Kind: v1.KindAddonObject, Name: c.Name}
	}
	return v1.ObjectRef{Kind: c.Kind, Name: c.Name}
}

// waitsOn is the first part of the App that p's spec names and that is not settled, as <Kind>/<name>; an addon
// binding's AddonObject matches the App's add-on part of that name (ADR-0362 Decision 4).
func waitsOn(a *v1.App, p v1.Object) string {
	if p == nil {
		return ""
	}
	for _, n := range gc.Names(p) {
		for _, c := range a.Status.Children {
			if c.Name != n.Ref.Name || settled(c) || c.State == v1.AppChildPruning {
				continue
			}
			if c.Kind == n.Ref.Kind || n.Ref.Kind == v1.KindAddonObject && partKey(a, c).Kind == v1.KindAddonObject {
				return string(c.Kind) + "/" + string(c.Name)
			}
		}
	}
	return ""
}

// remove draws one poll of a delete: each noted object, deleted, waiting with its reason, or kept (ADR-0362
// Decision 3). present holds the noted objects still there; kept, once the tree is gone, the stores that stay.
func (v *appView) remove(name v1.ObjectName, noted []v1.ObjectRef, present map[v1.ObjectRef]v1.Object, kept []v1.ObjectRef) error {
	st := v.styles()
	gone := 0
	for _, ref := range noted {
		if _, here := present[ref]; !here {
			gone++
		}
	}
	build := func(fold bool) []string {
		var rows [][]string
		folded := 0
		for _, ref := range noted {
			id := string(ref.Kind) + "/" + string(ref.Name)
			if _, timed := v.since[id]; !timed {
				v.since[id] = v.start // each object's time counts from the delete
			}
			o, here := present[ref]
			switch {
			case !here && fold:
				v.clock(id, true)
				folded++
			case !here:
				rows = append(rows, []string{st.ok.Render("✓") + " " + id, st.ok.Render("deleted"), v.clock(id, true)})
			default:
				rows = append(rows, []string{st.warn.Render("◐") + " " + id,
					st.warn.Render("waiting") + " " + st.dim.Render(termSafe(holdReason(o, noted, present))), v.clock(id, false)})
			}
		}
		if folded > 0 {
			rows = append(rows, []string{st.ok.Render("✓") + fmt.Sprintf(" %d more deleted", folded), "", ""})
		}
		for _, ref := range kept {
			rows = append(rows, []string{st.link.Render("■") + " " + string(ref.Kind) + "/" + string(ref.Name), st.link.Render("kept"), ""})
		}
		title := st.title.Render("● funcd app delete") +
			st.dim.Render(fmt.Sprintf("   %s · %d of %d gone · %s", name, gone, len(noted), span(v.now().Sub(v.start))))
		return []string{title, "", table(rows)}
	}
	return v.drawFitting(build, st)
}

// holdReason is why a noted object stays: its own Ready reason, else InUse naming a noted object still present that
// holds it as the GC's deletion protections read it (gc.Uses), else none, as for one that an object outside the App
// holds.
func holdReason(o v1.Object, noted []v1.ObjectRef, present map[v1.ObjectRef]v1.Object) string {
	if s, ok := o.(v1.StatusObject); ok {
		if c, ok := s.GetStatus().Conditions.Get("Ready"); ok && c.Status != v1.ConditionTrue && c.Reason != "" {
			return c.Reason
		}
	}
	for _, ref := range noted {
		if u, here := present[ref]; here && u != o && gc.Uses(u, o) {
			return "InUse: " + string(ref.Kind) + "/" + string(ref.Name)
		}
	}
	return ""
}

// viewStyles are the view's lipgloss styles, in the palette.
type viewStyles struct {
	title, dim, muted, ok, warn, bad, link, box lipgloss.Style
}

func (v *appView) styles() viewStyles {
	s := func(c lipgloss.TerminalColor) lipgloss.Style { return v.r.NewStyle().Foreground(c) }
	p := palette()
	return viewStyles{
		title: s(p.accent).Bold(true), dim: s(p.dim), muted: s(p.dim).Faint(true), ok: s(p.ok),
		warn: s(p.warn), bad: s(p.bad), link: s(p.link),
		box: v.r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(p.accent).Padding(0, 2),
	}
}

// table aligns rows into columns, each as wide as its widest cell.
func table(rows [][]string) string {
	widths := map[int]int{}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], lipgloss.Width(cell))
		}
	}
	lines := make([]string, len(rows))
	for r, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = cell + strings.Repeat(" ", widths[i]-lipgloss.Width(cell))
		}
		lines[r] = strings.TrimRight(strings.Join(cells, "  "), " ")
	}
	return strings.Join(lines, "\n")
}

// drawFitting draws the box that build makes, folded when it is taller than the terminal, so a redraw stays on the
// screen unless the unsettled rows alone are taller than it.
func (v *appView) drawFitting(build func(fold bool) []string, st viewStyles) error {
	lines := build(false)
	v.last, v.folded = build, len(strings.Split(strings.Join(lines, "\n"), "\n"))+2 >= v.height()
	if v.folded {
		lines = build(true)
	}
	return v.draw(lines, st)
}

// draw redraws the box in place: it moves the cursor up over the last draw, clears to the end of the screen and
// writes the new box, its lines cut to the terminal's width so none wraps (ADR-0362 Decision 5).
func (v *appView) draw(lines []string, st viewStyles) error {
	box := st.box.MaxWidth(v.width()).Render(strings.Join(lines, "\n"))
	if box == v.box {
		return nil
	}
	var b strings.Builder
	if v.drawn > 0 {
		fmt.Fprintf(&b, "\x1b[%dA\x1b[J", v.drawn)
	}
	b.WriteString(box + "\n")
	v.box, v.drawn = box, strings.Count(box, "\n")+1
	_, err := io.WriteString(v.out, b.String())
	return err
}

// done leaves the last draw on screen, unfolded since no redraw follows it, and the next output starts below it.
func (v *appView) done() error {
	var err error
	if v.live && v.folded && v.last != nil {
		err = v.draw(v.last(false), v.styles())
	}
	v.drawn, v.folded = 0, false
	return err
}
