package dropserver

import (
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func memoryWindow(node string, now time.Time) dbx.Params {
	return dbx.Params{"node": node, "since": now.Add(-24 * time.Hour).Unix(), "until": now.Unix()}
}

// nodeMemoryHistory returns the measurement at each UTC hour's highest
// aggregate usage. Keep the timestamp and VM count from that same sample.
func nodeMemoryHistory(app core.App, node string, now time.Time) ([]dropapi.GuestMemory, error) {
	out := []dropapi.GuestMemory{}
	err := app.DB().NewQuery(`SELECT at, used_mib, running_vms FROM (
  SELECT at, used_mib, running_vms, ROW_NUMBER() OVER
  (PARTITION BY at/3600 ORDER BY used_mib DESC,at DESC) AS rank
  FROM drop_node_memory WHERE node={:node} AND at>{:since} AND at<={:until}
 ) WHERE rank=1 ORDER BY at`).Bind(memoryWindow(node, now)).All(&out)
	return out, err
}

func nodeMemoryPeak(app core.App, nodeID string, now time.Time) (int, bool, error) {
	var row struct {
		Peak int `db:"peak"`
	}
	err := app.DB().NewQuery(`SELECT COALESCE(MAX(used_mib),-1) AS peak FROM drop_node_memory
 WHERE node={:node} AND at>{:since} AND at<={:until}`).Bind(memoryWindow(nodeID, now)).One(&row)
	return row.Peak, row.Peak >= 0, err
}

func saveGuestMemory(app core.App, node string, m *dropapi.GuestMemory, now time.Time) error {
	if err := waitExec(app, "DELETE FROM drop_node_memory WHERE at <= {:since}", dbx.Params{"since": now.Add(-24 * time.Hour).Unix()}); err != nil {
		return err
	}
	return waitExec(app, `INSERT INTO drop_node_memory(node,at,used_mib,running_vms) VALUES({:node},{:at},{:used},{:vms})
 ON CONFLICT(node,at) DO NOTHING`, dbx.Params{"node": node, "at": m.At, "used": m.UsedMiB, "vms": m.RunningVMs})
}
