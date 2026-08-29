package json

import (
	"github.com/bytedance/sonic"
)

func String(v any) string {
	s, _ := sonic.MarshalString(v)
	return s
}
