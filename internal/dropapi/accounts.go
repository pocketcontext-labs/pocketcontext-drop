package dropapi

// Account and invite operations (milestone M3).

// Invite limits and defaults.
const (
	DefaultAppQuota      = 3
	MaxAppQuota          = 100
	DefaultInviteDays    = 7
	MaxInviteDays        = 30
	MaxEmailLength       = 254
	DefaultListLimit     = 100
	MaxListLimit         = 500
	InviteStateOpen      = "open"
	InviteStateUsed      = "used"
	InviteStateRevoked   = "revoked"
	InviteStateExpired   = "expired"
	InviteListFilterAll  = "all"
	InviteListFilterOpen = "open"
)

// MeInput is the empty input of "me".
type MeInput struct{}

// Me is the signed-in account. Admin is true for role admin.
type Me struct {
	Developer             bool   `json:"developer"`
	ReleaseSelection      bool   `json:"release_selection,omitempty"`
	OfficialCatalog       bool   `json:"official_catalog,omitempty"`
	TrialRemainingSeconds *int64 `json:"trial_remaining_seconds,omitempty"`
	Hosted                bool   `json:"hosted"`
	ID                    string `json:"id"`
	Email                 string `json:"email"`
	Role                  Role   `json:"role"`
	Admin                 bool   `json:"admin"`
	Owner                 bool   `json:"owner"`
	AppQuota              int    `json:"app_quota"`
}

// UserListInput selects one page of accounts, newest first.
type UserListInput struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

// User is an account as admins see it. SignedIn is false until the first
// sign-in (core prunes such accounts after 15 minutes).
type User struct {
	Developer bool   `json:"developer"`
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      Role   `json:"role"`
	AppQuota  int    `json:"app_quota"`
	Suspended bool   `json:"suspended"`
	Owner     bool   `json:"owner"`
	SignedIn  bool   `json:"signed_in"`
	InvitedBy string `json:"invited_by"`
	Created   string `json:"created"`
}

// UserList is a page of accounts.
type UserList struct {
	Users   []User `json:"users"`
	HasMore bool   `json:"has_more"`
}

// UserSetInput changes one account, named by id or email. Absent fields stay.
type UserSetInput struct {
	User      string `json:"user"`
	Role      *Role  `json:"role,omitempty"`
	AppQuota  *int   `json:"app_quota,omitempty"`
	Suspended *bool  `json:"suspended,omitempty"`
}

// UserResult is the changed account.
type UserResult struct {
	User User `json:"user"`
}

// InviteCreateInput invites one address. Role defaults to user, AppQuota to
// DefaultAppQuota and ExpiresDays to DefaultInviteDays.
type InviteCreateInput struct {
	Email       string `json:"email"`
	Role        Role   `json:"role,omitempty"`
	AppQuota    *int   `json:"app_quota,omitempty"`
	ExpiresDays int    `json:"expires_days,omitempty"`
}

// Invite is one invite. State is derived: open, used, revoked or expired.
type Invite struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      Role   `json:"role"`
	AppQuota  int    `json:"app_quota"`
	State     string `json:"state"`
	InvitedBy string `json:"invited_by"`
	ExpiresAt string `json:"expires_at"`
	UsedAt    string `json:"used_at"`
	UsedBy    string `json:"used_by"`
	RevokedAt string `json:"revoked_at"`
	Created   string `json:"created"`
}

// InviteCreateResult is the new invite and the sign-in URL the admin shares.
// The URL only opens the reader's sign-in with the address filled in; it
// carries no credential (the invitee signs in with the emailed code).
type InviteCreateResult struct {
	Invite    Invite `json:"invite"`
	SignInURL string `json:"sign_in_url"`
}

// InviteListInput selects invites: State "all" (default) or "open".
type InviteListInput struct {
	State  string `json:"state,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

// InviteList is a page of invites.
type InviteList struct {
	Invites []Invite `json:"invites"`
	HasMore bool     `json:"has_more"`
}

// InviteRevokeInput names the invite to revoke.
type InviteRevokeInput struct {
	Invite string `json:"invite"`
}

// InviteResult is the changed invite.
type InviteResult struct {
	Invite Invite `json:"invite"`
}
