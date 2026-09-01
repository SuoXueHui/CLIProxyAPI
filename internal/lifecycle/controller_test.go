package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestControllerAdmissionAndTransitions(t *testing.T) {
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}
	controller := NewController(ModeStandby)
	controller.SetHooks(Hooks{
		Activate: func(context.Context) (Components, error) {
			record("activate")
			return Components{WriterLeaseHeld: true}, nil
		},
		BeginDrain:   func() { record("drain") },
		ResumeActive: func(context.Context) error { record("resume"); return nil },
		Deactivate:   func(context.Context) error { record("deactivate"); return nil },
	})

	if _, admitted := controller.AdmitProxy(); admitted {
		t.Fatal("standby admitted proxy traffic")
	}
	status, errTransition := controller.Transition(context.Background(), ModeServingReadOnly, 0)
	if errTransition != nil || status.Mode != ModeServingReadOnly {
		t.Fatalf("standby -> serving-readonly = %+v, %v", status, errTransition)
	}
	done, admitted := controller.AdmitProxy()
	if !admitted {
		t.Fatal("serving-readonly rejected proxy traffic")
	}
	if got := controller.Status().ActiveRequests; got != 1 {
		t.Fatalf("active requests = %d, want 1", got)
	}
	done()

	status, errTransition = controller.Transition(context.Background(), ModeActive, status.Generation)
	if errTransition != nil || status.Mode != ModeActive {
		t.Fatalf("serving-readonly -> active = %+v, %v", status, errTransition)
	}
	status, errTransition = controller.Transition(context.Background(), ModeDraining, status.Generation)
	if errTransition != nil || status.Mode != ModeDraining {
		t.Fatalf("active -> draining = %+v, %v", status, errTransition)
	}
	if _, admitted := controller.AdmitProxy(); admitted {
		t.Fatal("draining admitted new proxy traffic")
	}
	status, errTransition = controller.Transition(context.Background(), ModeStandby, status.Generation)
	if errTransition != nil || status.Mode != ModeStandby {
		t.Fatalf("draining -> standby = %+v, %v", status, errTransition)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"activate", "drain", "deactivate"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
}

func TestControllerDrainingWaitsForAdmittedRequests(t *testing.T) {
	controller := NewController(ModeActive)
	controller.SetHooks(Hooks{Deactivate: func(context.Context) error { return nil }})
	done, admitted := controller.AdmitProxy()
	if !admitted {
		t.Fatal("active rejected proxy traffic")
	}
	status, errDrain := controller.Transition(context.Background(), ModeDraining, 0)
	if errDrain != nil {
		t.Fatalf("active -> draining error = %v", errDrain)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, errStandby := controller.Transition(ctx, ModeStandby, status.Generation); !errors.Is(errStandby, context.DeadlineExceeded) {
		t.Fatalf("draining -> standby error = %v, want deadline", errStandby)
	}
	done()
	if _, errStandby := controller.Transition(context.Background(), ModeStandby, 0); errStandby != nil {
		t.Fatalf("draining -> standby after completion error = %v", errStandby)
	}
}

func TestControllerServingReadOnlyWaitsBeforeStandby(t *testing.T) {
	controller := NewController(ModeServingReadOnly)
	done, admitted := controller.AdmitProxy()
	if !admitted {
		t.Fatal("serving-readonly rejected proxy traffic")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, errStandby := controller.Transition(ctx, ModeStandby, 0); !errors.Is(errStandby, context.DeadlineExceeded) {
		t.Fatalf("serving-readonly -> standby error = %v, want deadline", errStandby)
	}
	done()
	if _, errStandby := controller.Transition(context.Background(), ModeStandby, 0); errStandby != nil {
		t.Fatalf("serving-readonly -> standby after completion error = %v", errStandby)
	}
}

func TestControllerRejectsStaleGenerationAndActivationFailure(t *testing.T) {
	controller := NewController(ModeServingReadOnly)
	controller.SetHooks(Hooks{Activate: func(context.Context) (Components, error) { return Components{}, errors.New("lease busy") }})
	initial := controller.Status()
	if _, errTransition := controller.Transition(context.Background(), ModeActive, initial.Generation+1); !errors.Is(errTransition, ErrGenerationConflict) {
		t.Fatalf("stale generation error = %v", errTransition)
	}
	if _, errTransition := controller.Transition(context.Background(), ModeActive, initial.Generation); errTransition == nil {
		t.Fatal("activation failure was ignored")
	}
	if got := controller.Status().Mode; got != ModeServingReadOnly {
		t.Fatalf("mode after activation failure = %s", got)
	}
}

func TestParseMode(t *testing.T) {
	for input, want := range map[string]Mode{
		"": ModeActive, "active": ModeActive, "standby": ModeStandby,
		"serving-readonly": ModeServingReadOnly, "draining": ModeDraining,
	} {
		got, errParse := ParseMode(input)
		if errParse != nil || got != want {
			t.Fatalf("ParseMode(%q) = %q, %v; want %q", input, got, errParse, want)
		}
	}
	if _, errParse := ParseMode("invalid"); errParse == nil {
		t.Fatal("invalid mode accepted")
	}
}
