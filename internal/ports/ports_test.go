package ports

import (
	"context"
	"errors"
	"testing"
)

func TestStaticList_ReturnsCopy(t *testing.T) {
	t.Parallel()

	listPorts := StaticList([]Info{{Name: "/dev/ttyACM0", IsUSB: true}}, nil)
	got, err := listPorts(context.Background())
	if err != nil {
		t.Fatalf("StaticList() error = %v", err)
	}
	if got[0].Name != "/dev/ttyACM0" {
		t.Fatalf("got[0].Name = %q, want %q", got[0].Name, "/dev/ttyACM0")
	}
	got[0].Name = "mutated"

	again, err := listPorts(context.Background())
	if err != nil {
		t.Fatalf("StaticList() second call error = %v", err)
	}
	if got, want := again[0].Name, "/dev/ttyACM0"; got != want {
		t.Fatalf("source was mutated: got %q, want %q", got, want)
	}
}

func TestStaticList_Error(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	listPorts := StaticList(nil, wantErr)
	_, err := listPorts(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("StaticList() error = %v, want %v", err, wantErr)
	}
}
