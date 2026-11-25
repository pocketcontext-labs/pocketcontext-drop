package client

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/agent"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/buildinfo"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/render"
)

// agentValueFlags are the options of `drop agent`; each overrides its
// DROP_AGENT_* variable (docs/agent.md).
var agentValueFlags = []string{"server", "host", "node", "secret-file", "desired", "reported", "artifacts", "state-dir",
	"allowed-host-suffix", "ssm-prefix", "fake-aws", "input", "previous", "socket"}

// exitRefused is the exit code of `drop agent run|check|render` when
// desired.json holds something only the operator can fix (entries Drop did
// not create, another host). systemd's RestartPreventExitStatus=3 keeps the
// unit from restarting into the same refusal.
const exitRefused = 3

// runAgent is `drop agent run|check|render`. It is dispatched before any
// project is resolved: it never opens a database, starts a server or reads
// the local key.
func runAgent(a *args) error {
	if a.has("db") || a.has("url") {
		return fail(2, "drop agent takes no --db or --url; it talks to DROP_AGENT_SERVER")
	}
	if len(a.pos) != 1 {
		return fail(2, "usage: drop agent run|check|render|host-services [options]; see `drop help agent`")
	}
	o := agent.Overrides{}
	for _, name := range agentValueFlags {
		if v, ok := a.flags[name]; ok {
			if v == "" {
				return fail(2, "--%s needs a value", name)
			}
			o[name] = v
		}
	}
	sub := a.pos[0]
	if sub != "host-services" && a.has("socket") {
		return fail(2, "--socket is an option of `drop agent host-services`")
	}
	switch sub {
	case "host-services":
		cfg, err := agent.LoadHostServicesConfig(os.Getenv, o)
		if err != nil {
			return fail(2, "agent host-services: %v", err)
		}
		logger := log.New(os.Stderr, "drop agent host-services: ", log.LstdFlags)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := agent.RunHostServices(ctx, cfg, logger.Printf); err != nil {
			return fail(1, "agent host-services: %v", err)
		}
		logger.Printf("stopped")
		return nil
	case "run", "check":
		if a.has("input") || a.has("previous") {
			return fail(2, "--input and --previous are options of `drop agent render`")
		}
		cfg, err := agent.LoadConfig(os.Getenv, o, true)
		if err != nil {
			return fail(2, "agent: %v", err)
		}
		stores, err := openAgentStores(cfg)
		if err != nil {
			return err
		}
		if sub == "check" {
			res := agent.Check(context.Background(), cfg, stores, buildinfo.Version)
			Print(res, a.bools["pretty"])
			switch {
			case res.Refused:
				return &Fail{exitRefused, "agent check: desired.json needs the operator (see the checks above)"}
			case !res.OK:
				return fail(1, "agent check failed (see the checks above)")
			}
			return nil
		}
		secret, err := agent.ReadSecret(cfg.SecretFile)
		if err != nil {
			return fail(2, "agent: %v", err)
		}
		logger := log.New(os.Stderr, "drop agent: ", log.LstdFlags)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = agent.New(cfg, secret, buildinfo.Version, stores, logger.Printf).Run(ctx)
		if agent.IsRefusal(err) {
			return &Fail{exitRefused, "agent refused to continue: " + err.Error()}
		}
		if err != nil {
			return fail(1, "agent: %v", err)
		}
		logger.Printf("stopped")
		return nil
	case "render":
		if !a.has("input") {
			return fail(2, "usage: drop agent render --input FILE|- [--previous FILE]")
		}
		cfg, err := agent.LoadConfig(os.Getenv, o, false)
		if err != nil {
			return fail(2, "agent: %v", err)
		}
		var input []byte
		if a.flags["input"] == "-" {
			input, err = io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
		} else {
			input, err = os.ReadFile(a.flags["input"])
		}
		if err != nil {
			return fail(2, "--input: %v", err)
		}
		previousPath := a.flags["previous"]
		if previousPath == "" {
			previousPath = cfg.Desired
		}
		var previous []byte
		if previousPath != "" {
			if previous, err = os.ReadFile(previousPath); err != nil && !os.IsNotExist(err) {
				return fail(2, "--previous: %v", err)
			}
		}
		out, err := agent.RenderDry(input, previous, render.Limits{AllowedHostSuffix: cfg.AllowedHostSuffix, SSMPrefix: cfg.SSMPrefix})
		if agent.IsRefusal(err) {
			return &Fail{exitRefused, "agent render: " + err.Error()}
		}
		if err != nil {
			return fail(1, "agent render: %v", err)
		}
		Print(out, a.bools["pretty"])
		return nil
	}
	return fail(2, "unknown agent command %q; use run, check, render or host-services", sub)
}

// openAgentStores opens the read-only AWS views: --fake-aws DIR (loopback
// server only, checked by LoadConfig) or S3 and SSM through the default
// credential chain (DROP_AWS_REGION, DROP_S3_BUCKET).
func openAgentStores(cfg agent.Config) (agent.Stores, error) {
	var stores *awsx.Stores
	var err error
	if cfg.FakeAWS != "" {
		stores, err = awsx.OpenFakeDir(cfg.FakeAWS)
		if err != nil {
			return agent.Stores{}, fail(1, "--fake-aws: %v", err)
		}
		log.Printf("drop agent: using %s", stores.Description)
	} else {
		awsCfg, err := awsx.ConfigFromEnv(os.Getenv)
		if err != nil {
			return agent.Stores{}, fail(2, "agent: %v", err)
		}
		awsCfg.SSMPrefix = cfg.SSMPrefix
		if stores, err = awsx.OpenAWS(context.Background(), awsCfg); err != nil {
			return agent.Stores{}, fail(1, "agent: %v", err)
		}
	}
	return agent.Stores{Blobs: stores.Blobs, Secrets: stores.Secrets}, nil
}
