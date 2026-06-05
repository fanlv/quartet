package wechat

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"

	"rsc.io/qr"
)

// StartLogin kicks off a scan-to-login attempt. Returns:
//   - qrcode: opaque handle the frontend passes back to the status-poll API.
//   - imgBase64: base64-encoded PNG the frontend can render in <img src>.
//
// iLink's QRCodeImgContent is the URL string that has to be encoded INTO
// the QR image (not a ready-to-use image), so we run it through rsc.io/qr
// here rather than leaving QR rendering to the browser.
func StartLogin(ctx context.Context) (qrcode string, imgBase64 string, err error) {
	resp, err := ilink.FetchQRCode(ctx)
	if err != nil {
		return "", "", fmt.Errorf("fetch qr code: %w", err)
	}
	if resp.QRCode == "" || resp.QRCodeImgContent == "" {
		return "", "", fmt.Errorf("iLink returned empty qr fields")
	}

	code, err := qr.Encode(resp.QRCodeImgContent, qr.L)
	if err != nil {
		return "", "", fmt.Errorf("render qr png: %w", err)
	}
	return resp.QRCode, base64.StdEncoding.EncodeToString(code.PNG()), nil
}
