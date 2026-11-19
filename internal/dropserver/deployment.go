package dropserver

import (
	"bytes"
	"encoding/json"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Deployments. nibrunner acts on an instance only when its deploymentId
// changes (plan finding 7): a changed configuration under the same id does
// nothing. So every change to what Drop renders into the instance gives the
// app a new deployment number, deployment_seq, and with it a new deploymentId
// <nib_app_id>-dep-<n>:
//
//   - app.set, when one of instanceFields changes value;
//   - secret.set and secret.delete, always (a new version or a removed name
//     changes the instance environment);
//   - release.deploy (M5b), when the release changes.
//
// Not bumped: the name (not rendered), app.start and app.stop (only the
// desired state, which nibrunner applies to the current deployment), the disk
// (the volume, not the instance; fixed once placed), archive and restore (the
// instance leaves or returns unchanged). The node's desired_seq (M7) is a
// separate counter: it goes up on every change that alters a node's rendered
// document, including these.

// instanceFields are the apps fields rendered into nibrunner's instance.
// start_state stands for running versus on-request (the activation block).
var instanceFields = []string{
	"start_state", "idle_timeout_ms", "vcpu", "memory_mib", "http_port",
	"executable_name", "args", "env", "health_kind", "health_path",
}

// instanceConfigChanged reports whether a loaded app record's instance
// configuration differs from the stored one.
func instanceConfigChanged(app *core.Record) bool {
	original := app.Original()
	for _, field := range instanceFields {
		if !bytes.Equal(normalizedValue(original.Get(field)), normalizedValue(app.Get(field))) {
			return true
		}
	}
	return false
}

// normalizedValue encodes a field value so that equal configurations compare
// equal: JSON is re-encoded (sorted keys, no spacing) and an empty or null
// list or object is the same as none.
func normalizedValue(v any) []byte {
	raw, ok := v.(types.JSONRaw)
	if !ok {
		raw, _ = json.Marshal(v)
	}
	var decoded any
	if len(raw) == 0 || json.Unmarshal(raw, &decoded) != nil {
		return nil
	}
	switch x := decoded.(type) {
	case nil:
		return nil
	case []any:
		if len(x) == 0 {
			return nil
		}
	case map[string]any:
		if len(x) == 0 {
			return nil
		}
	}
	out, _ := json.Marshal(decoded)
	return out
}

// bumpDeployment gives an app the next deployment number (saved with the
// record by the caller).
func bumpDeployment(app *core.Record) error {
	seq := app.GetInt("deployment_seq")
	if seq >= appspec.MaxDeploymentSeq {
		return Conflict(dropapi.CodeDeploymentLimit, "This app has used every deployment number. Create a new app.")
	}
	app.Set("deployment_seq", seq+1)
	return nil
}

// deploymentID is an app record's current deployment id.
func deploymentID(app *core.Record) string {
	return appspec.DeploymentID(app.GetString("nib_app_id"), app.GetInt("deployment_seq"))
}
