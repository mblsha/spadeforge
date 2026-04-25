package flasher

import (
	"reflect"
	"testing"

	"github.com/mblsha/spadeforge/internal/spadeloader/job"
)

func TestOpenFPGALoaderArgsDefaultsToRAM(t *testing.T) {
	t.Parallel()

	got := openFPGALoaderArgs("alchitry_au", "/tmp/design.bit", "")
	want := []string{"-b", "alchitry_au", "/tmp/design.bit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("openFPGALoaderArgs() = %#v, want %#v", got, want)
	}
}

func TestOpenFPGALoaderArgsFlashAddsWriteFlash(t *testing.T) {
	t.Parallel()

	got := openFPGALoaderArgs("alchitry_au", "/tmp/design.bit", job.ProgramTargetFlash)
	want := []string{"-b", "alchitry_au", "-f", "/tmp/design.bit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("openFPGALoaderArgs() = %#v, want %#v", got, want)
	}
}
