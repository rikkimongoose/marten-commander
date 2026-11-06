package volumes

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeDrives struct{ err error }

func (f fakeDrives) drives() (uint32, error) { return 1<<2 | 1<<3 | 1<<4 | 1<<5 | 1<<25, f.err }
func (fakeDrives) driveType(root string) uint32 {
	switch root {
	case `C:\`:
		return 3
	case `D:\`:
		return 2
	case `Z:\`:
		return 4
	case `E:\`:
		return 1
	default:
		return 0
	}
}
func (fakeDrives) label(root string) (string, error) {
	if root == `D:\` {
		return "", errors.New("no media")
	}
	if root == `C:\` {
		return "System", nil
	}
	return "", nil
}

func TestWindowsDriveEnumerationWithLabelsRemovableAndNetwork(t *testing.T) {
	items, err := listDrives(context.Background(), fakeDrives{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Root != `C:\` || items[0].Name != "System" || items[1].Root != `D:\` || items[1].Warning == "" || items[2].Root != `Z:\` {
		t.Fatalf("wrong drives: %+v", items)
	}
	if !reflect.DeepEqual(driveRoots(1|1<<25), []string{`A:\`, `Z:\`}) {
		t.Fatal("drive bitmask decoding")
	}
	if len(driveRoots(0)) != 0 {
		t.Fatal("empty bitmask")
	}
	if _, err := listDrives(context.Background(), fakeDrives{errors.New("failed")}); err == nil {
		t.Fatal("lost enumeration error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := listDrives(ctx, fakeDrives{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
