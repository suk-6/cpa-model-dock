# CPA store release contract

`make package` builds the five requested platforms and writes ZIPs, SHA-256
manifests and a **schema-2** direct store sample to `dist/registry.json`.
See [building](../docs/building.md) for prerequisites, subsets and exact names.

Host the generated registry and ZIPs over HTTPS, after regenerating with
`BASE_URL` set to the directory containing the archives. The manifest's `size`
is the ZIP byte count, and `sha256` hashes the ZIP, not its contained library.
Add the registry URL to CPA's `plugins.store-sources`. The built-in official
source remains present. The generated manifest does not claim official listing,
a published source repository, or an unsupported minimum-version field.

```yaml
plugins:
  enabled: true
  dir: plugins
  store-sources:
    - https://plugins.example.com/cpa-model-dock/0.1.0/registry.json
  configs:
    cpa-model-dock:
      enabled: true
      priority: 1000
      state-dir: /var/lib/cliproxyapi/cpa-model-dock
      catalog-refresh-interval: 5m
      guard-uncovered-endpoints: true
```

This is a subtree to merge into the host's config, not a complete config file.
Store installation writes
`<plugins.dir>/<goos>/<goarch>/cpa-model-dock-v<version>.<ext>` and records the
selected store version inside `plugins.configs.cpa-model-dock.store`. Preserve
that host-owned mapping. The library inside the downloaded ZIP has the
unversioned root filename `cpa-model-dock.<ext>`.

If applying for the official schema-1 GitHub release catalog later, supply a real
`repository: https://github.com/<owner>/<repo>` plus `id`, `name`, `description`,
and `author`. Publish ZIPs named
`cpa-model-dock_<version>_<goos>_<goarch>.zip` and `checksums.txt` on that repo's
release (this project: `https://github.com/suk-6/cpa-model-dock`). Schema 1 does
not support direct artifact URLs; use the generated schema 2 for self hosting.

Verified against CPA v8.0.44
[registry validation](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/registry.go#L166),
[install layout and ZIP validation](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/install.go#L312),
[host config types](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/config/config_types.go#L27),
and the official [store catalog](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store/blob/main/README.md).
