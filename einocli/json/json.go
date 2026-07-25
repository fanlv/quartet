package json

import (
	"github.com/bytedance/sonic"
)

func String(v any) string {
	s, _ := sonic.MarshalString(v)
	return s
}

func Marshal(v any) ([]byte, error) {
	return sonic.Marshal(v)
}

func Unmarshal(data []byte, v any) error {
	return sonic.Unmarshal(data, v)
}
