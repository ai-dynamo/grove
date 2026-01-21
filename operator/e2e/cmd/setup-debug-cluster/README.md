# setup-debug-cluster

Creates a local K3D cluster identical to the one used in E2E tests, with Grove operator and Kai scheduler pre-installed.

## Usage

```bash
# From this directory
go run .

# Or build and run
go build
./setup-debug-cluster
```

## Options

```
--name                 Cluster name (default: "grove-e2e-cluster")
--worker-nodes         Number of worker nodes (default: 30)
--verbose, -v          Enable verbose logging
--quiet, -q            Suppress non-error output
--help                 Show all options
```

## Teardown

Press `Ctrl+C` if running interactively, or:

```bash
k3d cluster delete grove-e2e-cluster
```
