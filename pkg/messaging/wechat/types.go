package wechat

import (
	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"
)

// CredentialsProvider supplies the list of iLink credentials currently
// loadable from disk. Manager / Replier call this lazily so scan-to-login
// can refresh credentials at runtime (via Manager.Restart()) without any
// restart of the backend process.
//
// For v1 only the first entry is used; extra entries are ignored with a
// warning. Multi-account support is tracked in doc §4.5.
type CredentialsProvider func() []*ilink.Credentials

// Option configures a Listener. No options are exposed for v1 — kept as a
// stable extension point so later phases (multi-account, custom message
// handlers, test injection) don't break the NewListener signature.
type Option func(*Listener)
