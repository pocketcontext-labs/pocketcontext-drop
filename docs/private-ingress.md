# Opening custom apps

Use **Open** in Drop or visit the app’s HTTPS root directly. A custom app has a generated hostname such as `dev-….pocketcontext.store`. Anyone with the URL can reach the running executable. The app can require its own sign-in, as Crew and Atlas do.

Developer accounts create custom apps from an executable file or download URL implementing `--drop-manifest`. Ownership still governs deployment, release artifacts, settings and secrets. An administrator can publish a submitted release to the catalog.

## Node upgrades

Upgrade the control plane and the node agent on every node serving custom apps. A public-route-capable agent reports `versions.customPublicIngress`. The control plane then reconciles existing custom apps on that node to the same root routing used by catalog instances. The apps keep their slugs, volumes and releases. During a rolling upgrade, older agents receive their previous route format until upgraded. Complete the rollout before using direct links to apps on those nodes.

Custom release bytes remain encrypted at rest. The control plane supplies the decryption key only to the assigned node through its authenticated desired document.
