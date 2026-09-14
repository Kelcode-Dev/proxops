package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/GizzmoShifu/proxmox-operator/internal/app"
	"github.com/GizzmoShifu/proxmox-operator/internal/adopt"
)

// runAdopt invokes the adopt package against a configured cluster and renders
// a human-readable summary. Returns ("", nil) on zero findings so the caller
// can fall back to printing "no drift".
func runAdopt(ctx context.Context, agent *app.Agent, cluster string, gitRoot string, log *slog.Logger) (string, error) {
	pve, pErr := agent.PVEClientFor(cluster)
	if pErr != nil {
		return "", pErr
	}
	cfgCluster, ok := agent.Config().PVE.Cluster(cluster)
	if !ok {
		return "", fmt.Errorf("cluster %q is not in pve.clusters", cluster)
	}
	res, aErr := adopt.Run(ctx, pve, cluster, cfgCluster.Nodes, gitRoot)
	if aErr != nil {
		return "", aErr
	}
	out := res.Summary()
	if len(res.Logs) > 0 {
		out += "\n"
		for _, l := range res.Logs {
			out += "  " + l + "\n"
		}
	}
	return out, nil
}
