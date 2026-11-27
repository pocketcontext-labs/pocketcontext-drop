package dropserver

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Node, placement and status operations (M7). Nodes and placements are
// admin-only: an admin adds a node (its agent's sign-in identity and a
// generated password shown once), changes its state, and places any
// account's app on it. status is every account's view of its own app's
// runtime phase; admins also see the node's name.

func init() {
	register("status", appStatus)
	register("node.list", nodeList)
	register("node.create", nodeCreate)
	register("node.rotate_secret", nodeRotateSecret)
	register("node.set_state", nodeSetState)
	register("placement.set", placementSet)
	register("placement.clear", placementClear)
}

var ec2InstancePattern = regexp.MustCompile(`^i-[0-9a-f]{8,17}$`)

func jsonMap(r *core.Record, field string) map[string]any {
	out := map[string]any{}
	_ = r.UnmarshalJSONField(field, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func online(state *agentState, node *core.Record, now time.Time) bool {
	seen := state.lastSeen(node)
	return !seen.IsZero() && now.Sub(seen) < dropapi.NodeOnlineSeconds*time.Second
}

func (c *Ctx) nodeOut(r *core.Record) (dropapi.Node, error) {
	var placed int
	err := c.App.DB().NewQuery(`SELECT COUNT(*) FROM placements p JOIN apps a ON a.id=p.app
 WHERE p.node={:node} AND (a.developer_private=0 OR a.owner={:viewer})`).Bind(dbx.Params{"node": r.Id, "viewer": c.Auth.Id}).Row(&placed)
	if err != nil {
		return dropapi.Node{}, err
	}
	state := agentStateOf(c.App)
	history, err := nodeMemoryHistory(c.App, r.Id, state.clock())
	if err != nil {
		return dropapi.Node{}, err
	}
	seen := state.lastSeen(r)
	heartbeat := ""
	if !seen.IsZero() {
		heartbeat = seen.UTC().Format(time.RFC3339)
	}
	return dropapi.Node{
		ID: r.Id, Name: r.GetString("name"), HostID: r.GetString("host_id"),
		EC2InstanceID: r.GetString("ec2_instance_id"), PrivateIP: r.GetString("private_ip"),
		State: r.GetString("state"), DesiredSeq: r.GetInt("desired_seq"),
		Online: online(state, r, state.clock()), LastHeartbeat: heartbeat, LastReportAt: dateString(r, "last_report_at"),
		HostState: r.GetString("host_state"), AgentVersion: r.GetString("agent_version"),
		AcceptedRevision: r.GetString("accepted_revision"),
		Capacity:         jsonMap(r, "capacity"), Allocatable: jsonMap(r, "allocatable"), Versions: jsonMap(r, "versions"),
		MemoryHistory: history, Apps: placed, Created: dateString(r, "created"),
	}, nil
}

// findNode loads a node by id or name.
func (c *Ctx) findNode(ref string) (*core.Record, error) {
	ref = strings.TrimSpace(ref)
	if recordID.MatchString(ref) {
		if r, err := c.App.FindRecordById(dropapi.NodeCollection, ref); err == nil {
			return r, nil
		}
	}
	if dropapi.NodeNamePattern.MatchString(ref) {
		if r, err := c.App.FindFirstRecordByData(dropapi.NodeCollection, "name", ref); err == nil {
			return r, nil
		}
	}
	return nil, NotFound("Node")
}

func nodeList(c *Ctx, in *dropapi.NodeListInput) (any, error) {
	limit, offset, err := page(in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	records, err := c.App.FindRecordsByFilter(dropapi.NodeCollection, "", "name", limit+1, offset)
	if err != nil {
		return nil, err
	}
	out := dropapi.NodeList{Nodes: []dropapi.Node{}, HasMore: len(records) > limit}
	for i, r := range records {
		if i == limit {
			break
		}
		n, err := c.nodeOut(r)
		if err != nil {
			return nil, err
		}
		out.Nodes = append(out.Nodes, n)
	}
	return out, nil
}

func (c *Ctx) nodeSecret(node *core.Record, password string) (any, error) {
	n, err := c.nodeOut(node)
	if err != nil {
		return nil, err
	}
	return dropapi.NodeSecret{Node: n, Password: password, Identity: node.GetString("name"), Collection: dropapi.NodeCollection}, nil
}

func nodeCreate(c *Ctx, in *dropapi.NodeCreateInput) (any, error) {
	if !dropapi.NodeNamePattern.MatchString(in.Name) {
		return nil, Invalid("name", "A node name is 2-31 lowercase letters, digits and hyphens, starting with a letter.")
	}
	if !dropapi.HostIDPattern.MatchString(in.HostID) {
		return nil, Invalid("host_id", "host_id is nibrunner's hostId (reported.json): 1-63 letters, digits, '_' or '-'.")
	}
	if in.EC2InstanceID != "" && !ec2InstancePattern.MatchString(in.EC2InstanceID) {
		return nil, Invalid("ec2_instance_id", "ec2_instance_id looks like i-0123456789abcdef0.")
	}
	if in.PrivateIP != "" && net.ParseIP(in.PrivateIP) == nil {
		return nil, Invalid("private_ip", "private_ip must be an IP address.")
	}
	if _, err := c.App.FindFirstRecordByData(dropapi.NodeCollection, "name", in.Name); err == nil {
		return nil, Conflict(dropapi.CodeNodeExists, "A node named "+in.Name+" exists.")
	}
	if _, err := c.App.FindFirstRecordByData(dropapi.NodeCollection, "host_id", in.HostID); err == nil {
		return nil, Conflict(dropapi.CodeNodeExists, "Another node has this host_id.")
	}
	collection, err := c.App.FindCachedCollectionByNameOrId(dropapi.NodeCollection)
	if err != nil {
		return nil, err
	}
	password := security.RandomString(dropapi.NodePasswordLength)
	node := core.NewRecord(collection)
	node.Set("name", in.Name)
	node.Set("host_id", in.HostID)
	node.Set("ec2_instance_id", in.EC2InstanceID)
	node.Set("private_ip", in.PrivateIP)
	node.Set("state", dropapi.NodeActive)
	node.Set("desired_seq", 0)
	node.SetPassword(password)
	if err := c.App.Save(node); err != nil {
		return nil, err
	}
	// The password is never in an event.
	c.Event(Event{Detail: map[string]any{"node": node.Id, "name": in.Name, "host_id": in.HostID}})
	return c.nodeSecret(node, password)
}

func nodeRotateSecret(c *Ctx, in *dropapi.NodeRotateSecretInput) (any, error) {
	node, err := c.findNode(in.Node)
	if err != nil {
		return nil, err
	}
	password := security.RandomString(dropapi.NodePasswordLength)
	node.SetPassword(password) // also ends the node's current tokens
	if err := c.App.Save(node); err != nil {
		return nil, err
	}
	agentStateOf(c.App).forget(node.Id)
	c.Event(Event{Detail: map[string]any{"node": node.Id, "name": node.GetString("name")}})
	return c.nodeSecret(node, password)
}

func nodeSetState(c *Ctx, in *dropapi.NodeSetStateInput) (any, error) {
	node, err := c.findNode(in.Node)
	if err != nil {
		return nil, err
	}
	switch in.State {
	case dropapi.NodeActive, dropapi.NodeDraining, dropapi.NodeDisabled:
	default:
		return nil, Invalid("state", "state must be active, draining or disabled.")
	}
	previous := node.GetString("state")
	node.Set("state", in.State)
	if in.State == dropapi.NodeDisabled && previous != dropapi.NodeDisabled {
		node.RefreshTokenKey() // a disabled node's tokens stop working now
	}
	if err := c.App.Save(node); err != nil {
		return nil, err
	}
	c.Event(Event{Detail: map[string]any{"node": node.Id, "name": node.GetString("name"), "state": in.State, "previous_state": previous}})
	n, err := c.nodeOut(node)
	if err != nil {
		return nil, err
	}
	return dropapi.NodeResult{Node: n}, nil
}

func placementOf(app core.App, appID string) (*core.Record, error) {
	var records []*core.Record
	if err := app.RecordQuery("placements").AndWhere(dbx.HashExp{"app": appID}).Limit(1).All(&records); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return records[0], nil
}

func placementOut(p, node *core.Record) dropapi.Placement {
	return dropapi.Placement{App: p.GetString("app"), Node: node.Id, NodeName: node.GetString("name"),
		Generation: p.GetInt("generation"), Updated: dateString(p, "updated")}
}

// dropReportsElsewhere deletes an app's node reports from nodes other than
// keep (all of them when keep is empty): a node the app left no longer
// reports it.
func dropReportsElsewhere(app core.App, appID, keep string) error {
	q := app.DB().Delete("node_reports", dbx.HashExp{"app": appID})
	if keep != "" {
		q = app.DB().Delete("node_reports", dbx.And(dbx.HashExp{"app": appID}, dbx.Not(dbx.HashExp{"node": keep})))
	}
	_, err := q.Execute()
	return err
}

// checkPlacedAppRoom checks an owner's resize or restore. Only admins see
// node names and the resource usage of other tenants.
func checkPlacedAppRoom(app core.App, placed *core.Record) error {
	p, err := placementOf(app, placed.Id)
	if err != nil || p == nil {
		return err
	}
	node, err := app.FindRecordById(dropapi.NodeCollection, p.GetString("node"))
	if err != nil {
		return err
	}
	err = checkNodeRoom(app, node, placed)
	var refused *dropapi.Error
	if errors.As(err, &refused) && refused.Code == dropapi.CodeNodeTooSmall {
		return Conflict(dropapi.CodeNodeTooSmall, "The node has insufficient capacity for this app.")
	}
	return err
}

// checkNodeRoom refuses a placement when the app, together with the other
// (not archived) apps already placed on node, needs more vCPUs or memory than
// the node's reported guest capacity (an unknown capacity skips the
// check). A stopped or on-request app still counts: it can start at any time
// without another placement.
func checkNodeRoom(app core.App, node, placed *core.Record) error {
	_, err := nodeMemoryRoom(app, node, placed)
	return err
}

// nodeMemoryRoom returns the unreserved MiB after admission, while enforcing
// both CPU and RAM commitments for all placements (including sleeping apps).
func nodeMemoryRoom(app core.App, node, placed *core.Record) (int, error) {
	vcpu, memory := allocatable(node)
	if vcpu < 0 && memory < 0 {
		return 0, nil
	}
	need := placed.GetInt("vcpu")
	needMemory := placed.GetInt("memory_mib")
	if (vcpu >= 0 && need > vcpu) || (memory >= 0 && needMemory > memory) {
		return 0, Conflict(dropapi.CodeNodeTooSmall, "The app needs more vCPUs or memory than node "+node.GetString("name")+" can allocate.")
	}
	// Capacity is the whole guest budget. Allocatable has already deducted
	// live guests, which are also present in the reservations query below.
	totalCPU, totalMemory := nodeResources(node, "capacity")
	if totalCPU < 0 {
		totalCPU = vcpu
	}
	if totalMemory < 0 {
		totalMemory = memory
	}
	var used struct {
		VCPU   int `db:"vcpu"`
		Memory int `db:"memory"`
	}
	err := app.DB().NewQuery(`SELECT COALESCE(SUM(MAX(a.vcpu, (reservation + 3) / 4)), 0) AS vcpu,
		COALESCE(SUM(MAX(a.memory_mib, reservation * 256)), 0) AS memory
		FROM placements p JOIN (
			SELECT apps.*, CASE WHEN json_extract(NULLIF(local_resize,''),'$.outcome') = 'pending'
				THEN json_extract(local_resize,'$.target') ELSE 0 END AS reservation FROM apps
		) a ON a.id = p.app
		WHERE p.node = {:node} AND p.app != {:app} AND a.archived = FALSE`).
		Bind(dbx.Params{"node": node.Id, "app": placed.Id}).One(&used)
	if err != nil {
		return 0, err
	}
	if (totalCPU >= 0 && used.VCPU+need > totalCPU) || (totalMemory >= 0 && used.Memory+needMemory > totalMemory) {
		return 0, Conflict(dropapi.CodeNodeTooSmall, fmt.Sprintf("Node %s has room for %d vCPUs and %d MiB; its other apps already take %d vCPUs and %d MiB, and this app needs %d and %d.",
			node.GetString("name"), totalCPU, totalMemory, used.VCPU, used.Memory, need, needMemory))
	}
	return totalMemory - used.Memory - needMemory, nil
}

func placementSet(c *Ctx, in *dropapi.PlacementSetInput) (any, error) {
	app, err := c.findApp(in.App, false)
	if err != nil {
		return nil, err
	}
	if pendingDiskRequest(app) {
		return nil, resizeBusy()
	}
	if accountsOf(c.App).hosted {
		owner, err := c.App.FindRecordById("users", app.GetString("owner"))
		if err != nil {
			return nil, err
		}
		if hostedAdmin(c.App, owner) && !(app.GetBool("developer_private") && owner.GetBool("developer")) {
			return nil, Forbidden("Only user-owned apps can be placed.")
		}
	}
	if app.GetBool("archived") {
		return nil, Conflict(dropapi.CodeAppArchived, "This app is archived. Restore it first.")
	}
	node, err := c.findNode(in.Node)
	if err != nil {
		return nil, err
	}
	if app.GetBool("developer_private") && jsonMap(node, "versions")["privateIngress"] != true {
		return nil, Conflict(dropapi.CodeNodeUnavailable, "This node does not support private developer apps.")
	}
	if node.GetString("state") != dropapi.NodeActive {
		return nil, Conflict(dropapi.CodeNodeUnavailable, "Node "+node.GetString("name")+" is "+node.GetString("state")+": only an active node takes apps.")
	}
	if err := checkNodeRoom(c.App, node, app); err != nil {
		return nil, err
	}
	placement, err := placementOf(c.App, app.Id)
	if err != nil {
		return nil, err
	}
	result := dropapi.PlacementResult{}
	switch {
	case placement == nil:
		collection, err := c.App.FindCachedCollectionByNameOrId("placements")
		if err != nil {
			return nil, err
		}
		placement = core.NewRecord(collection)
		placement.Set("app", app.Id)
		placement.Set("node", node.Id)
		placement.Set("generation", 1)
		placement.Set("created_by", c.Auth.Id)
		if err := c.App.Save(placement); err != nil {
			return nil, err
		}
	case placement.GetString("node") != node.Id:
		result.Moved, result.PreviousNode = true, placement.GetString("node")
		placement.Set("node", node.Id)
		placement.Set("generation", placement.GetInt("generation")+1)
		if err := c.App.Save(placement); err != nil {
			return nil, err
		}
	}
	if err := dropReportsElsewhere(c.App, app.Id, node.Id); err != nil {
		return nil, err
	}
	// The disk is fixed from now on: the volume is created on the node.
	if app.GetDateTime("first_placed_at").IsZero() {
		app.Set("first_placed_at", time.Now().UTC())
		if err := c.App.Save(app); err != nil {
			return nil, err
		}
	}
	result.Placement = placementOut(placement, node)
	detail := map[string]any{"node": node.Id, "generation": placement.GetInt("generation"), "moved": result.Moved}
	if result.Moved {
		detail["previous_node"] = result.PreviousNode
	}
	c.Event(Event{App: app.Id, Detail: detail})
	return result, nil
}

func placementClear(c *Ctx, in *dropapi.PlacementClearInput) (any, error) {
	app, err := c.findApp(in.App, false)
	if err != nil {
		return nil, err
	}
	if pendingDiskRequest(app) {
		return nil, resizeBusy()
	}
	placement, err := placementOf(c.App, app.Id)
	if err != nil {
		return nil, err
	}
	if placement == nil {
		return nil, NotFound("Placement")
	}
	nodeID := placement.GetString("node")
	if err := c.App.Delete(placement); err != nil {
		return nil, err
	}
	if err := dropReportsElsewhere(c.App, app.Id, ""); err != nil {
		return nil, err
	}
	c.Event(Event{App: app.Id, Detail: map[string]any{"node": nodeID}})
	return dropapi.PlacementClearResult{App: app.Id, ClearedNode: nodeID}, nil
}

// failurePhases are reported phases that stay as they are while a new
// deployment is pending (the node could not get to it).
var failurePhases = map[string]bool{"staging_failed": true, "secret_failed": true, "failed": true, "unhealthy": true}

func appStatus(c *Ctx, in *dropapi.StatusInput) (any, error) {
	app, err := c.findApp(in.App, false)
	if err != nil {
		return nil, err
	}
	out := dropapi.Status{
		App: app.Id, DesiredState: app.GetString("desired_state"), DeploymentID: deploymentID(app),
		Hostname: appHostname(app, servicesOf(c.App).AppDomain),
	}
	placement, err := placementOf(c.App, app.Id)
	if err != nil {
		return nil, err
	}
	var node *core.Record
	if placement != nil {
		if node, err = c.App.FindRecordById(dropapi.NodeCollection, placement.GetString("node")); err != nil {
			return nil, err
		}
		out.Placed = true
		if c.Admin {
			out.Node = node.GetString("name")
		}
	}
	switch {
	case app.GetBool("archived"):
		out.Phase, out.Message = dropapi.PhaseArchived, "The app is archived."
		return out, nil
	case app.GetString("current_release") == "":
		out.Phase, out.Message = dropapi.PhaseNoRelease, "Upload and deploy a release first."
		return out, nil
	case node == nil:
		out.Phase, out.Message = dropapi.PhaseUnplaced, "Waiting for an admin to place the app on a node."
		return out, nil
	}
	var rows []*core.Record
	if err := c.App.RecordQuery("node_reports").AndWhere(dbx.HashExp{"node": node.Id, "app": app.Id}).Limit(1).All(&rows); err != nil {
		return nil, err
	}
	state := agentStateOf(c.App)
	if seen := state.lastSeen(node); !seen.IsZero() {
		out.LastSeen = seen.UTC().Format(time.RFC3339)
	}
	if len(rows) == 0 {
		out.Phase, out.Message = dropapi.PhasePending, "Waiting for the node to start the app."
		return out, nil
	}
	row := rows[0]
	out.Phase, out.Message = row.GetString("phase"), row.GetString("message")
	out.ReportedDeploymentID = row.GetString("deployment_id")
	if app.GetBool("generic_beta") && out.ReportedDeploymentID == out.DeploymentID && app.GetString("owner") == c.Auth.Id {
		out.LogTail = row.GetString("log_tail")
		if setup := storedFirstRun(row); setup.Available(state.clock()) {
			out.FirstRun = setup
		}
	}
	out.RestartCount = row.GetInt("restart_count")
	if row.GetBool("has_exit_code") {
		code := row.GetInt("last_exit_code")
		out.LastExitCode = &code
	}
	out.LastHook = storedHook(row)
	out.ConvergedAt = row.GetString("converged_at")
	out.ObservedAt = dateString(row, "observed_at")
	switch {
	case !online(state, node, state.clock()):
		out.Phase, out.Message = dropapi.PhaseUnknown, "The node has not reported for a while."
	case out.ReportedDeploymentID != out.DeploymentID && !failurePhases[out.Phase]:
		out.Phase, out.Message = dropapi.PhaseDeploying, "Rolling out deployment "+out.DeploymentID+"."
	}
	if in.VerifyURL && out.ReportedDeploymentID == out.DeploymentID &&
		(out.Phase == "running" || out.Phase == "sleeping") {
		if app.GetString("beta_digest") != "" && (app.GetString("executable_name") != "crew" || app.GetBool("generic_beta") || app.GetString("crew_owner_email") == "") {
			var executable string
			if app.GetBool("generic_beta") {
				release, err := c.App.FindRecordById("releases", app.GetString("current_release"))
				if err != nil {
					return nil, err
				}
				if release.GetString("archive_sha256") != "" {
					executable = release.GetString("executable_path")
				}
			}
			out.URLReady = betaURLReady(c.Request.Context(), servicesOf(c.App).AppDomain, app.GetString("slug"), out.Hostname, executable, readinessClient)
		} else if app.GetString("crew_owner_email") != "" {
			out.URLReady = crewURLReady(c.Request.Context(), servicesOf(c.App).AppDomain, app.GetString("slug"), out.Hostname, readinessClient)
		}
	}

	return out, nil
}
