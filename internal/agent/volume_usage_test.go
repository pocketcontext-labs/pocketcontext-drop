package agent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestReportIncludesOnlyCurrentCanonicalVolumeUsage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	when := now.Format(time.RFC3339)
	intent := []appIntent{{AppID: appA, DeploymentID: appA + "-dep-1"}}
	for _, tc := range []struct {
		volume, measured string
		want             bool
	}{
		{appA + "-vol", when, true},
		{appB + "-vol", when, false},
		{appA + "-vol", now.Add(-6 * time.Minute).Format(time.RFC3339), false},
	} {
		var r nibReported
		raw := fmt.Sprintf(`{"reportedAt":%q,"instances":[{"appId":%q,"deploymentId":%q,"state":"running"}],"volumes":[{"appId":%q,"volumeId":%q,"state":"ready","usage":{"totalBytes":100,"usedBytes":75,"measuredAt":%q}}]}`, when, appA, intent[0].DeploymentID, appA, tc.volume, tc.measured)
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		got := appReports(intent, &r, "", now)[0].VolumeUsage
		if (got != nil) != tc.want || got != nil && *got.UsedBytes != 75 {
			t.Fatalf("volume %s at %s: %+v", tc.volume, tc.measured, got)
		}
	}
}
