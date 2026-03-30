package ports

import "context"

type Info struct {
	Name         string
	IsUSB        bool
	VID          string
	PID          string
	SerialNumber string
}

type ListFunc func(ctx context.Context) ([]Info, error)

func StaticList(list []Info, err error) ListFunc {
	return func(_ context.Context) ([]Info, error) {
		if err != nil {
			return nil, err
		}
		return clone(list), nil
	}
}

func clone(list []Info) []Info {
	out := make([]Info, len(list))
	copy(out, list)
	return out
}
