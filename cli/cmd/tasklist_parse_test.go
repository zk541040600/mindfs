package main

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestParseTasklistImageName(t *testing.T) {
	name, err := parseTasklistImageName([]byte(`"mindfs.exe","21560","Console","1","18,244 K"`), 21560)
	if err != nil {
		t.Fatalf("parseTasklistImageName returned error: %v", err)
	}
	if name != "mindfs.exe" {
		t.Fatalf("expected mindfs.exe, got %q", name)
	}
}

func TestParseTasklistImageNameRejectsLocalizedNoTaskMessage(t *testing.T) {
	_, err := parseTasklistImageName([]byte("信息: 没有运行的任务匹配指定标准。\r\n"), 21560)
	if !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("expected os.ErrProcessDone, got %v", err)
	}
}

func TestParseTasklistImageNameRejectsMismatchedPID(t *testing.T) {
	_, err := parseTasklistImageName([]byte(`"mindfs.exe","21561","Console","1","18,244 K"`), 21560)
	if !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("expected os.ErrProcessDone, got %v", err)
	}
}

func TestNormalizeTaskRootFirstArgs(t *testing.T) {
	got := normalizeTaskRootFirstArgs([]string{"mindfs", "-task", "12", "-next"})
	want := []string{"-task", "12", "-next", "mindfs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestTaskCLIActionDefaultsToStatus(t *testing.T) {
	if got := taskCLIAction(false, false, false); got != "status" {
		t.Fatalf("action = %q, want status", got)
	}
	if got := taskCLIAction(false, true, true); got != "" {
		t.Fatalf("conflicting action = %q, want empty", got)
	}
}
