# ProxOps example GitOps repository

A complete, minimal ProxOps GitOps repository template: **copy this whole tree
into a new git repository, point it at your PVE cluster, and run ProxOps from
inside it.** It demonstrates the canonical repository-first workflow and every
resource kind ProxOps manages.

## What you get

```
proxops.yaml                    # OPTIONAL process-wide config (defaults work
                                # without it): log / reconcile / listen /
                                # data-dir + bootstrap credentials for
                                # SOPS-less clusters
clusters/
  example/
    config.yaml                 # cluster-local config: PVE endpoint, node
                                #   allowlist, SOPS credentials reference
    secrets.sops.yaml           # SOPS/age-encrypted PVE + git credentials
                                #   (ALL VALUES SYNTHETIC — replace with your
                                #   own; never commit a real private age key)
    resources.yaml              # composition: resource files the "example"
                                #   cluster consumes
iso/base/…                      # 1 base ISO (shared, downloadable artifact)
ctt/base/…                      # 1 base CTTemplate (shared, downloadable)
diskimage/base/…                # 1 base DiskImage (cloud image for VM disks)
vm/example/…                    # 6 VMs: talos worker (base ISO cdrom),
                                #   clone-vm (TemplateVM clone), app-vm
                                #   (depends-on escape hatch), cloudinit-vm
                                #   (DiskImage-seeded + inline cloud-init data),
                                #   gpu-passthrough-01 (host PCI passthrough,
                                #   M13.2 pci-devices), sops-cloudinit-01
                                #   (M13.2 SOPS ssh-key-refs / ci-password-ref)
lxc/example/…                   # 1 LXC bootstrapped from the base CTTemplate
templatevm/example/…            # 1 TemplateVM (a VM promoted to PVE template)
templatect/example/…            # 1 TemplateCT (a CT promoted to PVE template)
```

The PVE endpoints, node names and credentials in the examples are **synthetic
fictions** (`pve.example.invalid`, `example@pam`, …). Replace them with your
real values before using the tree against a live cluster.

## First run (repository-first)

```sh
# 1. Copy this tree into a new git repository:
mkdir my-gitops && cp -r <operator>/examples/* my-gitops/ && cd my-gitops
git init -b main && git add -A && git commit -m "initial proxops gitops repo"

# 2. Point the cluster at YOUR PVE:
#    edit clusters/example/config.yaml (base-url + node allowlist)
#    rebuild clusters/example/secrets.sops.yaml for your age key (see the
#    operator's docs/sops-credentials.md — the private key stays OUTSIDE the
#    repository; the committed SOPS file only carries encrypted values + the
#    public age recipient)

#    Dev shortcut: skip SOPS entirely — drop the secrets-file/secrets block
#    from config.yaml and export PROXOPS_PVE_TOKEN_VALUE instead.

# 3. ProxOps now knows everything from the directory it is launched from:
export SOPS_AGE_KEY_FILE=~/.local/share/proxops/example.age   # if SOPS mode
proxops diff              # read-only: shows the plan against PVE
proxops apply --dry-run   # same, rendered through the full pipeline
proxops apply             # converge
```

No `--config`, no work-tree path: `proxops` discovers the repository from the
current directory and reads its optional process-wide `proxops.yaml` plus each
`clusters/<cluster>/config.yaml`.

## The composition

`clusters/example/resources.yaml` explicitly lists every resource file the
"example" cluster reconciles (NOT a Kustomization — ProxOps has no Kustomize
semantics). Bases under `<kind>/base/` are shared by reference (any cluster
may list the very same file); cluster-specific files under
`<kind>/example/` belong to this cluster's composition.

PVE id-space note: the kinds that carry a numeric PVE id (VM, LXC, TemplateVM,
TemplateCT) share ONE per-node integer pool. These examples pin well-separated
ids: 142 (VM), 400 (app-vm), 410 (cloudinit-vm), 900 (TemplateVM), 910
(TemplateCT), 9000 (LXC), 901 (clone-vm). A TemplateCT shares the LXC id
space (PVE lists both as `type="lxc"`), exactly as a TemplateVM shares the qm
id space. M13.2: `vm/example/gpu-passthrough-01.yaml` (vmid 412) demonstrates
`spec.hardware.pci-devices` (host PCI passthrough → PVE `hostpci<N>`), and
`vm/example/sops-cloudinit-01.yaml` (vmid 413) demonstrates
`spec.cloud-init-data.ssh-key-refs` + `ci-password-ref` (structured SOPS
cloud-init references — no inline public key or plaintext password in the
manifest; the SOPS doc carries `cloud-init.ssh-keys` /
`cloud-init.passwords`). The artifact kinds (`ISO`, `CTTemplate`, `DiskImage`) have **no**
numeric PVE id — their identity is `(node, storage, filename)` on the PVE
side and `metadata.name` on the ProxOps side.
