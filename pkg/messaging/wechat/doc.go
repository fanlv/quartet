// Package wechat provides personal-WeChat integration via the iLink
// protocol. It bridges messages between `pkg/messaging/wechat/ilink` (the protocol
// layer) and the platform-neutral `pkg/messaging` abstractions consumed by
// `cmd/web/handler/im_gateway.go`.
//
// # Protocol layer source
//
// Files under `pkg/messaging/wechat/ilink/` and portions of `pkg/messaging/wechat/{cdn,media}.go`
// are ported from https://github.com/fastclaw-ai/weclaw at **v0.8.0**.
//
// The iLink protocol is private to Tencent and may change without notice.
// Keep the protocol layer in sync with upstream: run scripts/update-weclaw.sh,
// review the result, and bump the pin when a new release addresses a protocol
// break. Upstream diff URL template:
//
//	https://github.com/fastclaw-ai/weclaw/compare/v0.8.0...{new-tag}
//
// Upstream weclaw is MIT-licensed; every ported file carries an attribution
// header pointing back to upstream.
package wechat
