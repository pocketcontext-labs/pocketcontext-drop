// Nodes: tunnel_id is the node's own Cloudflare Tunnel, from its agent's
// reports; the control plane points each placed app's hostname at it
// (dns.go). pool is where Drop Beta places new apps: "beta" for every
// account, "owner" for the instance owner's own apps first; empty keeps the
// node out of placement (node.set_pool).
migrate((app) => {
  const nodes = app.findCollectionByNameOrId("nodes");
  nodes.fields.add(new TextField({name: "tunnel_id", max: 36, pattern: "^$|^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"}));
  nodes.fields.add(new SelectField({name: "pool", values: ["beta", "owner"], maxSelect: 1}));
  app.save(nodes);
}, () => { throw new Error("Restore the pre-upgrade backup."); });
