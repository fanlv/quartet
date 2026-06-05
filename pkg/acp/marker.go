package acp

// ACPChildMarkerEnv / ACPChildMarkerValue identify subprocesses that the
// quartet process itself spawned as ACP agent children. The pair is
// injected into cmd.Env by NewConn (pkg/acp/conn.go) and inherited across
// fork/exec, so the whole subprocess tree (including sh/node wrappers that
// npx creates) carries it.
//
// Orphan cleanup on Linux (pkg/acp/process_unix.go) uses this marker as the
// authoritative "this tree is ours to kill" check — keyword-based cmdline
// matching is only a cheap prefilter and cannot, on its own, authorize a
// kill(-pgid) on a PPID==1 root. Without the marker check, substring
// collisions (e.g. "openclaw" matching /etc/sysop/mongoosev3-agent/plugin/
// openclaw-collector/openclaw-collector, or "coco" matching an unrelated
// user command in an ssh session) would escalate to killing system-level
// service process groups.
const (
	ACPChildMarkerEnv   = "QUARTET_ACP_CHILD"
	ACPChildMarkerValue = "1"
)
