package lark

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"github.com/fanlv/quartet/pkg/logger"

	ws "github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	// larkWSInitialRetryInterval is the delay after the first connection failure.
	// Kept short so transient blips recover quickly.
	larkWSInitialRetryInterval = 2 * time.Second
	// larkWSMaxRetryInterval caps the exponential backoff.
	larkWSMaxRetryInterval = 5 * time.Minute
	// larkWSConnWatchInterval is how often we check if the conn is still alive.
	larkWSConnWatchInterval = 5 * time.Second
	// larkWSErrorEscalationThreshold is the number of consecutive failures
	// after which the log level escalates from WARN to ERROR. This helps
	// operators notice persistent connectivity issues that outlast transient
	// network blips.
	larkWSErrorEscalationThreshold = 5
)

// startWebSocket runs the Lark SDK WebSocket with a cancellable lifecycle.
//
// We deliberately do not call larkws.Client.Start: in v3.5.3 it starts the
// connection and then blocks forever on select{}, while its ping and reconnect
// loops also ignore ctx. That makes Manager.Restart leak goroutines and a
// WebSocket fd. Instead, we reuse the SDK's unexported connect/disconnect and
// writeMessage methods via go:linkname: the SDK still owns endpoint discovery,
// frame decoding and event dispatch, but Quartet owns cancellation, ping and
// reconnect backoff.
func (l *Listener) startWebSocket(ctx context.Context) error {
	if l.client == nil {
		return fmt.Errorf("lark websocket client not initialized")
	}
	setLarkWSAutoReconnect(l.client, false)

	consecutiveFailures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := larkWSConnect(l.client, ctx)
		if err != nil {
			if _, ok := err.(*larkws.ClientError); ok {
				return err
			}
			consecutiveFailures++
			delay := retryBackoff(consecutiveFailures, larkWSInitialRetryInterval, larkWSMaxRetryInterval)
			if ctx.Err() == nil {
				if consecutiveFailures >= larkWSErrorEscalationThreshold {
					logger.Errorf(ctx, "[lark] websocket connect failed %d consecutive times (>=%d), retrying in %s: %v",
						consecutiveFailures, larkWSErrorEscalationThreshold, delay, err)
				} else {
					logger.Warnf(ctx, "[lark] websocket connect failed (attempt %d), retrying in %s: %v", consecutiveFailures, delay, err)
				}
			}
			if err := sleepContext(ctx, delay); err != nil {
				return err
			}
			continue
		}

		// Connected successfully — reset backoff counter.
		consecutiveFailures = 0

		if err := l.runConnectedWebSocket(ctx); err != nil {
			return err
		}
		// Disconnected — start with a short first retry since the connection
		// was previously healthy (likely a transient issue).
		consecutiveFailures++
		delay := retryBackoff(consecutiveFailures, larkWSInitialRetryInterval, larkWSMaxRetryInterval)
		logger.Warnf(ctx, "[lark] websocket disconnected, reconnecting in %s", delay)
		if err := sleepContext(ctx, delay); err != nil {
			return err
		}
	}
}

// retryBackoff computes an exponential backoff duration with jitter:
//
//	delay = min(initial * 2^(attempt-1), max) + jitter(0..25% of delay)
func retryBackoff(attempt int, initial, maxDelay time.Duration) time.Duration {
	if attempt <= 0 {
		return initial
	}
	delay := float64(initial) * math.Pow(2, float64(attempt-1))
	if delay > float64(maxDelay) {
		delay = float64(maxDelay)
	}
	// Add up to 25% jitter to avoid thundering herd.
	jitter := delay * 0.25 * rand.Float64()
	return time.Duration(delay + jitter)
}

func (l *Listener) runConnectedWebSocket(ctx context.Context) error {
	watch := time.NewTicker(larkWSConnWatchInterval)
	defer watch.Stop()

	pingTimer := time.NewTimer(larkWSPingInterval(l.client))
	defer pingTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			setLarkWSAutoReconnect(l.client, false)
			larkWSDisconnect(l.client, context.WithoutCancel(ctx))
			return ctx.Err()
		case <-watch.C:
			if !larkWSHasConn(l.client) {
				return nil
			}
		case <-pingTimer.C:
			if !larkWSHasConn(l.client) {
				return nil
			}
			if err := larkWSSendPing(l.client); err != nil && ctx.Err() == nil {
				logger.Warnf(ctx, "[lark] websocket ping failed: %v", err)
			}
			pingTimer.Reset(larkWSPingInterval(l.client))
		}
	}
}

func larkWSSendPing(client *larkws.Client) error {
	serviceID := larkWSServiceID(client)
	if serviceID == 0 {
		return nil
	}
	frame := larkws.NewPingFrame(serviceID)
	bs, err := frame.Marshal()
	if err != nil {
		return err
	}
	return larkWSWriteMessage(client, ws.BinaryMessage, bs)
}

func larkWSHasConn(client *larkws.Client) bool {
	mu := larkWSMutex(client)
	mu.Lock()
	defer mu.Unlock()
	v := reflect.ValueOf(client).Elem().FieldByName("conn")
	return !forceExported(v).IsNil()
}

func larkWSServiceID(client *larkws.Client) int32 {
	mu := larkWSMutex(client)
	mu.Lock()
	defer mu.Unlock()
	v := reflect.ValueOf(client).Elem().FieldByName("serviceID")
	raw := forceExported(v).String()
	id, _ := strconv.ParseInt(raw, 10, 32)
	return int32(id)
}

func larkWSPingInterval(client *larkws.Client) time.Duration {
	mu := larkWSMutex(client)
	mu.Lock()
	defer mu.Unlock()
	v := reflect.ValueOf(client).Elem().FieldByName("pingInterval")
	interval := forceExported(v).Interface().(time.Duration)
	if interval <= 0 {
		return 2 * time.Minute
	}
	return interval
}

func setLarkWSAutoReconnect(client *larkws.Client, enabled bool) {
	mu := larkWSMutex(client)
	mu.Lock()
	defer mu.Unlock()
	v := reflect.ValueOf(client).Elem().FieldByName("autoReconnect")
	forceExported(v).SetBool(enabled)
}

func larkWSMutex(client *larkws.Client) *sync.Mutex {
	v := reflect.ValueOf(client).Elem().FieldByName("mu")
	return forceExported(v).Addr().Interface().(*sync.Mutex)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func forceExported(v reflect.Value) reflect.Value {
	return reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
}

//go:linkname larkWSConnect github.com/larksuite/oapi-sdk-go/v3/ws.(*Client).connect
func larkWSConnect(client *larkws.Client, ctx context.Context) error

//go:linkname larkWSDisconnect github.com/larksuite/oapi-sdk-go/v3/ws.(*Client).disconnect
func larkWSDisconnect(client *larkws.Client, ctx context.Context)

//go:linkname larkWSWriteMessage github.com/larksuite/oapi-sdk-go/v3/ws.(*Client).writeMessage
func larkWSWriteMessage(client *larkws.Client, messageType int, data []byte) error
