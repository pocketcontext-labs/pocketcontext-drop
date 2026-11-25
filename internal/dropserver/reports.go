package dropserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Reports: POST /api/drop/agent/report (drop.report/v1, dropapi.Report).
//
//  1. The node's in-memory heartbeat is updated (heartbeat.go); its fields
//     are written only when they change or every five minutes.
//  2. Each app report is taken only for an app placed on this node (others
//     are listed as ignored) and written to node_reports only when its
//     phase, message, deployment, restarts, exit code or convergence time
//     changed. A phase change writes an app.phase event with actor_node,
//     except between running, sleeping and starting (an on-request app's
//     ordinary sleep and wake).
//  3. When nothing changed, the request writes nothing: no transaction runs.
//
// The answer carries the node's current desiredSeq and state, so an agent
// that missed a realtime event pulls again.

// quietPhases are the phases an on-request app moves between all day.
var quietPhases = []string{"running", "sleeping", "starting"}

func serveReport(re *core.RequestEvent, state *agentState) error {
	node, err := agentNode(re, state)
	if err != nil {
		return writeError(re, err)
	}
	body, err := io.ReadAll(http.MaxBytesReader(re.Response, re.Request.Body, dropapi.MaxReportBytes))
	if err != nil {
		return writeError(re, &dropapi.Error{Status: http.StatusRequestEntityTooLarge, Code: dropapi.CodeInvalidRequest, Message: "A report is at most 1 MiB."})
	}
	var report dropapi.Report
	if err := decodeStrict(body, &report); err != nil {
		return writeError(re, &dropapi.Error{Status: http.StatusBadRequest, Code: dropapi.CodeInvalidRequest, Message: "The body must be a drop.report/v1 document."})
	}
	if err := checkReport(&report); err != nil {
		return writeError(re, err)
	}
	if report.HostID != node.GetString("host_id") {
		return writeError(re, Conflict(dropapi.CodeHostMismatch, "This report is for another host than the node's: check the agent's node and nibrunner's host id."))
	}
	now := state.clock().UTC()
	nodeFields := reportNodeFields(&report)
	fingerprint := nodeFields.fingerprint()
	writeNode := state.seen(node.Id, fingerprint, now)

	ingest := func(app core.App, apply bool) (reportOutcome, error) {
		return ingestApps(app, node.Id, report.Apps, now, apply)
	}
	outcome, err := ingest(re.App, false)
	if err != nil {
		return writeError(re, err)
	}
	if writeNode || outcome.changes > 0 {
		err = re.App.RunInTransaction(func(tx core.App) error {
			fresh, err := tx.FindRecordById(dropapi.NodeCollection, node.Id)
			if err != nil {
				return err
			}
			if fresh.GetString("state") == dropapi.NodeDisabled || fresh.TokenKey() != node.TokenKey() {
				return Forbidden("This node no longer has access.")
			}
			if outcome, err = ingest(tx, true); err != nil {
				return err
			}
			if !writeNode {
				return nil
			}
			allocatableBefore := normalizedValue(fresh.Get("allocatable"))
			nodeFields.apply(fresh, now)
			if err := tx.Save(fresh); err != nil {
				return err
			}
			// A new allocatable can change which apps pass the check.
			if string(allocatableBefore) != string(normalizedValue(fresh.Get("allocatable"))) {
				_, err = syncNode(tx, fresh)
			}
			return err
		})
		if err != nil {
			return writeError(re, err)
		}
		if writeNode {
			state.persisted(node.Id, fingerprint, now)
		}
	}
	current, err := re.App.FindRecordById(dropapi.NodeCollection, node.Id)
	if err != nil {
		return writeError(re, err)
	}
	return re.JSON(http.StatusOK, dropapi.ReportResult{
		DesiredSeq: int64(current.GetInt("desired_seq")), State: current.GetString("state"),
		Accepted: outcome.accepted, Ignored: outcome.ignored,
	})
}

func reportInvalid(field, message string) *dropapi.Error {
	return Invalid(field, message)
}

// cleanMessage cuts a message to MaxReportMessage characters and replaces
// control characters (but newlines and tabs) with spaces.
func cleanMessage(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	s = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	if utf8.RuneCountInString(s) > dropapi.MaxReportMessage {
		s = string([]rune(s)[:dropapi.MaxReportMessage])
	}
	return s
}

func shortText(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// checkReport validates a report and cleans its messages in place.
func checkReport(r *dropapi.Report) error {
	if r.Schema != dropapi.ReportSchema {
		return reportInvalid("schema", "schema must be "+dropapi.ReportSchema+".")
	}
	if !dropapi.HostIDPattern.MatchString(r.HostID) {
		return reportInvalid("hostId", "hostId is not a nibrunner host id.")
	}
	if !dropapi.AgentVersionPattern.MatchString(r.AgentVersion) {
		return reportInvalid("agentVersion", "agentVersion must be 1-64 letters, digits, '.', '_', '+' or '-'.")
	}
	if r.DesiredSeq < 0 || r.DesiredSeq > appspec.MaxDeploymentSeq {
		return reportInvalid("desiredSeq", "desiredSeq is out of range.")
	}
	if !shortText(r.AcceptedRevision, 128) {
		return reportInvalid("acceptedRevision", "acceptedRevision is at most 128 characters.")
	}
	if h := r.Nibrunner; h != nil {
		if !shortText(h.State, 32) || !shortText(h.ReportedAt, 40) {
			return reportInvalid("nibrunner", "nibrunner state and reportedAt are short text.")
		}
		h.Message = cleanMessage(h.Message)
		for name, value := range map[string]map[string]any{"capacity": h.Capacity, "allocatable": h.Allocatable, "versions": h.Versions} {
			if raw, err := json.Marshal(value); err != nil || len(raw) > 2048 {
				return reportInvalid("nibrunner."+name, "nibrunner."+name+" is at most 2 KiB of JSON.")
			}
		}
	}
	if len(r.Apps) > dropapi.MaxReportApps {
		return reportInvalid("apps", fmt.Sprintf("A report has at most %d apps.", dropapi.MaxReportApps))
	}
	seen := map[string]bool{}
	for i := range r.Apps {
		a := &r.Apps[i]
		field := "apps[" + strconv.Itoa(i) + "]"
		if !appspec.AppIDPattern.MatchString(a.AppID) || seen[a.AppID] {
			return reportInvalid(field+".appId", "Each appId is a Drop app id, at most once.")
		}
		seen[a.AppID] = true
		if a.DeploymentID != "" && !appspec.DeploymentIDOK(a.AppID, a.DeploymentID) {
			return reportInvalid(field+".deploymentId", "deploymentId must be <appId>-dep-<n> or empty.")
		}
		if !slices.Contains(dropapi.ReportPhases, a.Phase) {
			return reportInvalid(field+".phase", "phase must be one of "+strings.Join(dropapi.ReportPhases, ", ")+".")
		}
		if a.RestartCount < 0 || a.RestartCount > 1<<30 {
			return reportInvalid(field+".restartCount", "restartCount is out of range.")
		}
		if a.LastExitCode != nil && (*a.LastExitCode < -1<<31 || *a.LastExitCode > 1<<31-1) {
			return reportInvalid(field+".lastExitCode", "lastExitCode is out of range.")
		}
		if !a.LastHook.Valid() {
			return reportInvalid(field+".lastHook", "Invalid lifecycle hook result.")
		}
		if !shortText(a.ConvergedAt, 40) {
			return reportInvalid(field+".convergedAt", "convergedAt is at most 40 characters.")
		}
		if err := checkHostUsage(a, field); err != nil {
			return err
		}
		a.Message = cleanMessage(a.Message)
	}
	return nil
}

// nodeFields are the nodes fields a report sets.
type nodeFields struct {
	AgentVersion     string `json:"agent_version"`
	AcceptedRevision string `json:"accepted_revision"`
	HostState        string `json:"host_state"`
	ReportedAt       string `json:"reported_at"`
	Capacity         any    `json:"capacity"`
	Allocatable      any    `json:"allocatable"`
	Versions         any    `json:"versions"`
	hasHost          bool
}

func reportNodeFields(r *dropapi.Report) nodeFields {
	f := nodeFields{AgentVersion: r.AgentVersion, AcceptedRevision: r.AcceptedRevision}
	if h := r.Nibrunner; h != nil {
		f.hasHost = true
		f.HostState, f.ReportedAt = h.State, h.ReportedAt
		f.Capacity, f.Allocatable, f.Versions = h.Capacity, h.Allocatable, h.Versions
	}
	return f
}

// fingerprint is what decides a write besides the clock. nibrunner's
// reportedAt changes with every nibrunner report, so it is left out: it is
// written with the rest.
func (f nodeFields) fingerprint() string {
	c := f
	c.ReportedAt = ""
	raw, _ := json.Marshal(c)
	return string(raw)
}

func (f nodeFields) apply(node *core.Record, now time.Time) {
	node.Set("agent_version", f.AgentVersion)
	node.Set("accepted_revision", f.AcceptedRevision)
	node.Set("last_heartbeat", now)
	if !f.hasHost {
		return
	}
	node.Set("host_state", f.HostState)
	if t, err := types.ParseDateTime(f.ReportedAt); err == nil && !t.IsZero() {
		node.Set("last_report_at", t)
	}
	for field, value := range map[string]any{"capacity": f.Capacity, "allocatable": f.Allocatable, "versions": f.Versions} {
		if value == nil {
			value = map[string]any{}
		}
		node.Set(field, value)
	}
}

type reportOutcome struct {
	accepted, changes int
	ignored           []string
}

// ingestApps compares the app reports of node with its node_reports rows
// and, with apply, writes the changed ones (and their events).
func ingestApps(app core.App, nodeID string, reports []dropapi.AppReport, now time.Time, apply bool) (reportOutcome, error) {
	out := reportOutcome{ignored: []string{}}
	placements, err := app.FindRecordsByFilter("placements", "node = {:node}", "", 0, 0, dbx.Params{"node": nodeID})
	if err != nil {
		return out, err
	}
	placed := map[string]*core.Record{} // nib_app_id -> apps record
	for _, p := range placements {
		if a, err := app.FindRecordById("apps", p.GetString("app")); err == nil {
			placed[a.GetString("nib_app_id")] = a
		}
	}
	rows, err := app.FindRecordsByFilter("node_reports", "node = {:node}", "", 0, 0, dbx.Params{"node": nodeID})
	if err != nil {
		return out, err
	}
	existing := map[string]*core.Record{}
	for _, r := range rows {
		existing[r.GetString("nib_app_id")] = r
	}
	collection, err := app.FindCachedCollectionByNameOrId("node_reports")
	if err != nil {
		return out, err
	}
	for _, r := range reports {
		appRecord := placed[r.AppID]
		if appRecord == nil {
			out.ignored = append(out.ignored, r.AppID)
			continue
		}
		out.accepted++
		row := existing[r.AppID]
		previousPhase := ""
		if row != nil {
			previousPhase = row.GetString("phase")
			if sameReport(row, r) && !hostUsageDue(row, r, now) {
				continue
			}
		}
		out.changes++
		if !apply {
			continue
		}
		if row == nil {
			row = core.NewRecord(collection)
			row.Set("node", nodeID)
			row.Set("nib_app_id", r.AppID)
		}
		row.Set("app", appRecord.Id)
		row.Set("deployment_id", r.DeploymentID)
		row.Set("phase", r.Phase)
		row.Set("message", r.Message)
		row.Set("restart_count", r.RestartCount)
		row.Set("has_exit_code", r.LastExitCode != nil)
		row.Set("last_exit_code", 0)
		if r.LastExitCode != nil {
			row.Set("last_exit_code", *r.LastExitCode)
		}
		row.Set("converged_at", r.ConvergedAt)
		row.Set("last_hook", r.LastHook)
		applyHostUsage(row, r, now)
		if previousPhase != r.Phase || row.GetDateTime("observed_at").IsZero() {
			row.Set("observed_at", now)
		}
		if err := app.Save(row); err != nil {
			return out, err
		}
		if previousPhase != r.Phase && !(slices.Contains(quietPhases, previousPhase) && slices.Contains(quietPhases, r.Phase)) {
			detail := map[string]any{"phase": r.Phase, "deployment_id": r.DeploymentID}
			if previousPhase != "" {
				detail["previous_phase"] = previousPhase
			}
			if err := WriteEvent(app, EventRecord{ActorNode: nodeID, App: appRecord.Id, Action: "app.phase", Detail: detail}); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

func storedHook(row *core.Record) *dropapi.HookResult {
	var hook *dropapi.HookResult
	_ = row.UnmarshalJSONField("last_hook", &hook)
	return hook
}

// sameReport reports whether a stored row already says what r says.
func sameReport(row *core.Record, r dropapi.AppReport) bool {
	exitSame := row.GetBool("has_exit_code") == (r.LastExitCode != nil) && (r.LastExitCode == nil || row.GetInt("last_exit_code") == *r.LastExitCode)
	return reflect.DeepEqual(storedHook(row), r.LastHook) && exitSame && row.GetString("deployment_id") == r.DeploymentID && row.GetString("phase") == r.Phase &&
		row.GetString("message") == r.Message && row.GetInt("restart_count") == r.RestartCount &&
		row.GetString("converged_at") == r.ConvergedAt
}
