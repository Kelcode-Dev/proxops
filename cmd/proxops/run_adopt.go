package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/GizzmoShifu/proxmox-operator/internal/adopt"
	"github.com/GizzmoShifu/proxmox-operator/internal/app"
	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
	"github.com/GizzmoShifu/proxmox-operator/internal/secrets"
)

// runAdopt invokes the adopt package against a configured cluster and renders
// a human-readable summary. Returns ("", nil) on zero findings so the caller
// can fall back to printing "no drift".
//
// M13.2: when the cluster declares a SOPS file, we pass the in-memory
// already-decrypted SOPS store to adopt (so plain adopt matches live
// ssh-keys to existing SOPS material, and --adopt-secrets collects new
// keys). The CLI (not adopt) performs the on-disk SOPS merge + encrypt
// step: adopt's zero-write invariant extends to the SOPS file as well,
// so this step runs AFTER RunWithOptions succeeds, and any error from
// the merge is surfaced to the operator (fail-closed: an incomplete SOPS
// merge would leave unresolvable ssh-key-refs in the committed manifests).
func runAdopt(ctx context.Context, agent *app.Agent, cluster string, gitRoot string, log *slog.Logger, adoptSecrets bool) (string, error) {
	pve, pErr := agent.PVEClientFor(cluster)
	if pErr != nil {
		return "", pErr
	}
	cfgCluster, ok := agent.Config().PVE.Cluster(cluster)
	if !ok {
		return "", fmt.Errorf("cluster %q is not in pve.clusters", cluster)
	}

	// M13.2: build in-memory Options from the already-resolved SOPS store.
	var opts adopt.Options
	if sc, ok := agent.Config().SopsResolved[cluster]; ok && sc.SourceFile != "" {
		// SopsResolved entries exist ONLY when a SOPS file was configured AND
		// successfully decrypted for the cluster. The cloud-init blocks may be
		// empty at first sight ("SOPS-backed but empty") — that is a different
		// operator state than "no SOPS store at all" and MUST be surfaced as
		// SOPS-backed, so re-running with --adopt-secrets imports unmatched keys
		// into the file rather than claiming the cluster has no store.
		sdoc := secrets.SecretsFile{
			Values:     map[string]string{},
			SSHKeys:    sc.CloudInitSSHKeys,
			Passwords:  sc.CloudInitPasswords,
			SourcePath: sc.SourceFile,
			Loaded:     true,
		}
		opts.SOPS = sdoc
		opts.AdoptSecrets = adoptSecrets
	} else if adoptSecrets {
		return "", fmt.Errorf("cluster %q has no secrets-file configured; --adopt-secrets requires a SOPS-backed cluster", cluster)
	}

	res, aErr := adopt.RunWithOptions(ctx, pve, cluster, cfgCluster.Nodes, gitRoot, opts)
	if aErr != nil {
		return "", aErr
	}
	out := res.Summary()

	// M13.2 SOPS merge step (only when --adopt-secrets AND new keys found).
	if adoptSecrets && len(res.NewSSHKeys) > 0 {
		msg, merr := mergeSOPSSSHKeys(agent.Config(), cluster, res.NewSSHKeys)
		if merr != nil {
			// Adopt succeeded (manifests on disk + gaps reported) but the
			// SOPS write failed. Surface both: the SOPS error AND what the
			// operator should expect next (the committed ssh-key-refs are
			// unresolvable until the merge lands; the next cycle will fail
			// closed with "ssh-key-ref ... missing in SOPS document" — that
			// is the fail-closed guarantee: a broken refs state is never
			// silently reconciled against PVE with wrong/sentinel keys).
			out += "\n" + msg
			return out, merr
		}
		out += "\n" + msg
	}

	if len(res.Logs) > 0 {
		out += "\n"
		for _, l := range res.Logs {
			out += "  " + l + "\n"
		}
	}
	return out, nil
}

// mergeSOPSSSHKeys performs the M13.2 SOPS re-encrypt step. The CLI is
// the sole on-disk SOPS writer:
//
//  1. decrypt the on-disk SOPS file fresh — so the M9 flat `secrets:` block
//     (PVE/git credentials) is preserved; in-memory SopsResolved only knows
//     about cloud-init material.
//
//  2. merge newSSHKeys into `cloud-init.ssh-keys`: existing names keep,
//     new names append (deterministic, no clobber of operator values).
//
//  3. read the existing age recipients — MUST keep them (dropping one
//     would lock out another operator who shares the file).
//
//  4. re-encrypt via `sops --encrypt` + atomic write (encrypt to a
//     sidecar, os.Rename over the destination; no plaintext on disk ever).
//
// Returns a human-readable message for the CLI summary; NEVER contains
// secret material.
func mergeSOPSSSHKeys(cfg *config.Config, cluster string, newSSHKeys map[string]string) (string, error) {
	cl, ok := cfg.PVE.Cluster(cluster)
	if !ok {
		return "adopt: SOPS merge failed", fmt.Errorf("cluster %q not found in config", cluster)
	}
	if cl.SecretsFile == "" {
		return "adopt: SOPS merge failed", fmt.Errorf("cluster %q has no secrets-file; cannot merge new SOPS ssh-keys", cluster)
	}
	doc, err := secrets.DecryptFile(cl.SecretsFile)
	if err != nil {
		return "adopt: SOPS merge failed", fmt.Errorf("re-decrypt %s for merge: %w", cl.SecretsFile, err)
	}
	merged := map[string]string{}
	for k, v := range doc.SSHKeys {
		merged[k] = v
	}
	added := []string{}
	for n, material := range newSSHKeys {
		if _, exists := merged[n]; exists {
			continue
		}
		merged[n] = material
		added = append(added, n)
	}
	doc.SSHKeys = merged
	sort.Strings(added)
	if len(added) == 0 {
		return "adopt: SOPS merge: no new ssh-keys to import (all names already in the cluster's secrets-file)", nil
	}
	recipients, err := secrets.ReadAgeRecipients(cl.SecretsFile)
	if err != nil {
		return "adopt: SOPS merge failed", err
	}
	if err := secrets.EncryptToFile(&doc, recipients, cl.SecretsFile); err != nil {
		return "adopt: SOPS merge failed", err
	}
	return formatSOPSSummary(len(added), added), nil
}

// formatSOPSSummary renders the M13.2 SOPS-merge summary block for the CLI.
// No secret material appears: only NEW KEY NAMES (which are reference
// identifiers, not values) and a note about where the encrypted data lives.
func formatSOPSSummary(n int, added []string) string {
	out := fmt.Sprintf("adopt: SOPS merge: imported %d new cloud-init.ssh-keys entry/entries into this cluster's secrets-file:\n", n)
	for _, name := range added {
		out += "  - " + schema.CloudInitSSHRefPrefix + name + "\n"
	}
	out += "existing age recipients are preserved (no operator lockout)."
	out += "\ncommit the updated secrets.sops.yaml together with the new"
	out += " ssh-key-refs manifests so the next cycle is already green."
	return out
}
