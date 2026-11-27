package dropserver

import (
	"net/http"
	"net/url"
	"regexp"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// These references identify the documents the operator has actually published.
type BetaLegal struct {
	TermsURL, PrivacyURL, TermsVersion string
}

var legalVersion = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$")

func (l BetaLegal) ready() bool {
	if !legalVersion.MatchString(l.TermsVersion) {
		return false
	}
	for _, raw := range []string{l.TermsURL, l.PrivacyURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return false
		}
	}
	return l.TermsURL != l.PrivacyURL
}

// Publish only the complete, approved settings used by the activation hook.
func registerLegalHeaders(e *core.ServeEvent) {
	e.Router.BindFunc(func(re *core.RequestEvent) error {
		if re.Request.Method == http.MethodGet && re.Request.URL.Path == "/api/bundle/auth-mode" && accountsOf(re.App).hosted {
			h := re.Response.Header()
			h.Set("Cache-Control", "no-store")
			if enabled, err := officialEnabled(re.App); err == nil && enabled {
				h.Set("X-Drop-Catalog", "official")
			}
			if legal := servicesOf(re.App).BetaLegal; legal.ready() {
				h.Set("X-Drop-Terms-URL", legal.TermsURL)
				h.Set("X-Drop-Privacy-URL", legal.PrivacyURL)
				h.Set("X-Drop-Terms-Version", legal.TermsVersion)
			}
		}
		return re.Next()
	})
}

func betaEnabled(app core.App) (bool, error) {
	services := servicesOf(app)
	if len(services.BetaNodes) != 0 || len(services.BetaOwnerNodes) != 0 {
		return true, nil
	}
	var enabled bool
	err := app.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM drop_beta_catalog)").Row(&enabled)
	return enabled, err
}

func markBetaEnrollment(app core.App, r *core.Record) error {
	if !accountsOf(app).hosted || adminAddress(app, lowerASCII(r.Email())) || r.GetString("beta_terms_version") != "" {
		return nil
	}
	enabled, err := betaEnabled(app)
	if err == nil && enabled {
		r.Set("beta_terms_pending", true)
	}
	return err
}

// Both public redemption paths call RecordAuthResponse after credential
// verification. Withhold the token until the acceptance is durable. A verified
// mailbox by itself never clears the persistent pending-enrollment marker.
func bindBetaTerms(app core.App) {
	app.OnRecordUpdate("users").BindFunc(func(e *core.RecordEvent) error {
		if !e.Record.Original().Verified() && e.Record.Verified() && !e.Record.GetBool("beta_terms_pending") {
			if err := markBetaEnrollment(e.App, e.Record); err != nil {
				return err
			}
		}
		return e.Next()
	})
	app.OnRecordAuthRequest("users").BindFunc(func(e *core.RecordAuthRequestEvent) error {
		if !e.Record.GetBool("beta_terms_pending") {
			return e.Next()
		}
		var rejection *dropapi.Error
		err := e.App.RunInTransaction(func(tx core.App) error {
			r, err := tx.FindRecordById("users", e.Record.Id)
			if err != nil {
				return err
			}
			if !r.GetBool("beta_terms_pending") {
				return nil
			}
			legal := servicesOf(tx).BetaLegal
			switch {
			case !legal.ready():
				rejection = &dropapi.Error{Status: http.StatusConflict, Code: "beta_activation_closed", Message: "New Beta accounts cannot activate until the Terms of Service and Privacy Policy are published."}
			case !r.Verified() || e.AuthMethod != core.MFAMethodOTP || e.Request.Header.Get("X-Drop-Accept-Terms") != legal.TermsVersion:
				rejection = &dropapi.Error{Status: http.StatusPreconditionRequired, Code: "terms_required", Message: "Read and accept the current Terms of Service, then request a new sign-in code."}
			default:
				r.Set("beta_terms_pending", false)
				r.Set("beta_terms_version", legal.TermsVersion)
				r.Set("beta_terms_accepted_at", types.NowDateTime())
			}
			if rejection != nil {
				// Keep a declined/abandoned first sign-in eligible for Core's
				// ordinary pending-account pruning and invitation recovery.
				r.SetVerified(false)
			}
			return tx.Save(r)
		})
		if err != nil {
			return err
		}
		if rejection != nil {
			return writeError(e.RequestEvent, rejection)
		}
		return e.Next()
	})
}
