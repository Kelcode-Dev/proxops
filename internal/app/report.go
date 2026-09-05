package app

import (
	"fmt"
	"strings"

	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
)

// renderPlan produces a human-readable --diff report from a plan.
//
// Shape (per line):
//
//	<what>   <kind>/<name>  <node>  id=<id>   <reason>
//
// Sections are grouped: "will create", "will update", "power", "will delete
// (prune)", "deferred (budget)", "skipped (untagged)".
func renderPlan(p *plan.Plan) string {
	if p == nil {
		return ""
	}
	var sb strings.Builder

	section := func(title string, acts []plan.Action) {
		if len(acts) == 0 {
			return
		}
		fmt.Fprintf(&sb, "=== %s (%d) ===\n", title, len(acts))
		for _, a := range acts {
			fmt.Fprintf(&sb, "  %-12s %-24s %-10s id=%d  %s\n",
				string(a.What), actionLabel(a), a.Node, a.ID, a.Reason)
		}
	}
	actions := make([]plan.Action, 0, len(p.Actions))
	creates := make([]plan.Action, 0, 4)
	updates := make([]plan.Action, 0, 4)
	powers := make([]plan.Action, 0, 4)
	prunes := make([]plan.Action, 0, 4)
	for _, a := range p.Actions {
		actions = append(actions, a)
		switch a.What {
		case plan.Create:
			creates = append(creates, a)
		case plan.Update:
			updates = append(updates, a)
		case plan.Start, plan.Stop:
			powers = append(powers, a)
		case plan.Delete:
			prunes = append(prunes, a)
		}
	}
	section("will create", creates)
	section("will update", updates)
	section("power changes", powers)
	section("will delete (prune)", prunes)
	section("deferred prunes (budget)", p.Deferred)
	section("skipped (no pveconform tag)", p.Skipped)

	if len(actions) == 0 && len(p.Deferred) == 0 && len(p.Skipped) == 0 {
		return ""
	}
	return sb.String()
}

func actionLabel(a plan.Action) string {
	if a.Kind == "" {
		return a.Name
	}
	return string(a.Kind) + "/" + a.Name
}
