package dropserver

import (
	"context"
	"sort"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dnsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Per-app DNS (Services.DNS): every placed, not archived app whose node
// reports a tunnel gets a proxied CNAME from its hostname to that tunnel, so
// any node can serve any app. Apps on a node without a tunnel get no record
// (a zone wildcard, if any, still serves them). Records whose app left, was
// archived or moved to a node without a tunnel are deleted. Only records with
// the dnsx.Managed comment are touched, and only names under the app domain.

const dnsInterval = 30 * time.Second

// desiredDNS maps each hostname that should have a record to its CNAME target.
func desiredDNS(app core.App) (map[string]string, error) {
	domain := servicesOf(app).AppDomain
	out := map[string]string{}
	if !ValidAppDomain(domain) {
		return out, nil
	}
	placements, err := app.FindAllRecords("placements")
	if err != nil {
		return nil, err
	}
	for _, p := range placements {
		node, err := app.FindRecordById(dropapi.NodeCollection, p.GetString("node"))
		if err != nil {
			return nil, err
		}
		tunnel := node.GetString("tunnel_id")
		if !dropapi.TunnelIDPattern.MatchString(tunnel) || node.GetString("state") == dropapi.NodeDisabled {
			continue
		}
		a, err := app.FindRecordById("apps", p.GetString("app"))
		if err != nil {
			return nil, err
		}
		if a.GetBool("archived") {
			continue
		}
		host := appHostname(a, domain)
		if host == "" || !strings.HasSuffix(host, "."+domain) || strings.Count(host, ".") != strings.Count(domain, ".")+1 {
			continue
		}
		out[host] = dnsx.Target(tunnel)
	}
	return out, nil
}

// syncDNS brings the zone's managed records to the desired set once. Nothing
// is deleted unless both the database and the zone were read completely.
func syncDNS(ctx context.Context, app core.App, zone dnsx.Provider) error {
	want, err := desiredDNS(app)
	if err != nil {
		return err
	}
	have, err := zone.Managed(ctx)
	if err != nil {
		return err
	}
	byName := map[string]dnsx.Record{}
	for _, r := range have {
		if _, dup := byName[r.Name]; dup || want[r.Name] == "" {
			if err := zone.Delete(ctx, r.ID); err != nil {
				return err
			}
			app.Logger().Info("Drop removed an app DNS record", "hostname", r.Name)
			continue
		}
		byName[r.Name] = r
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	var firstErr error
	for _, name := range names {
		target := want[name]
		if r, ok := byName[name]; ok {
			if r.Content != target {
				if err := zone.Update(ctx, r.ID, target); err != nil && firstErr == nil {
					firstErr = err
				}
				app.Logger().Info("Drop moved an app DNS record", "hostname", name, "target", target)
			}
			continue
		}
		taken, err := zone.Taken(ctx, name)
		if err != nil {
			return err
		}
		if taken {
			app.Logger().Warn("Drop left an app hostname alone: it has a DNS record Drop does not manage", "hostname", name)
			continue
		}
		if err := zone.Create(ctx, name, target); err != nil && firstErr == nil {
			firstErr = err
		}
		app.Logger().Info("Drop added an app DNS record", "hostname", name, "target", target)
	}
	return firstErr
}

func runDNSSync(ctx context.Context, app core.App) {
	zone := servicesOf(app).DNS
	if zone == nil {
		return
	}
	ticker := time.NewTicker(dnsInterval)
	defer ticker.Stop()
	for {
		if err := syncDNS(ctx, app, zone); err != nil && ctx.Err() == nil {
			app.Logger().Error("Drop app DNS sync failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
