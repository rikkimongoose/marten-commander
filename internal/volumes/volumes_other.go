//go:build !linux && !windows

package volumes

import (
	"context"
	"fmt"
)

type systemBackend struct{}

func (systemBackend) list(context.Context) ([]Volume, error) {
	return nil, fmt.Errorf("перечисление томов поддерживается только на Linux и Windows")
}
func (systemBackend) locate(context.Context, string) (Volume, error) {
	return Volume{}, fmt.Errorf("информация о томах недоступна на этой платформе")
}
func (systemBackend) space(string) (Space, error) {
	return Space{}, fmt.Errorf("информация об объёме недоступна")
}
