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
		PrepareReadOnly: func(context.Context) error { record("prepare-readonly"); return nil },
		Activate: func(context.Context) (Components, error) {
			record("activate")
			return Components{WriterLeaseHeld: true}, nil
		},
		BeginDrain: func() { record("drain") },
		ResumeActive: func(context.Context) (Components, error) {
			record("resume")
			return Components{WriterLeaseHeld: true}, nil
		},
		Deactivate: func(context.Context) (Components, error) { record("deactivate"); return Components{}, nil },
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
	want := []string{"prepare-readonly", "activate", "drain", "deactivate"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
}

func TestControllerPreparesReadOnlyBeforeAdmissionWithoutHoldingStateLock(t *testing.T) {
	controller := NewController(ModeStandby)
	prepareStarted := make(chan struct{})
	allowPrepare := make(chan struct{})
	controller.SetHooks(Hooks{PrepareReadOnly: func(context.Context) error {
		if got := controller.Status().Mode; got != ModeStandby {
			return errors.New("mode changed before read-only preparation completed")
		}
		close(prepareStarted)
		<-allowPrepare
		return nil
	}})

	transitionDone := make(chan error, 1)
	go func() {
		_, errTransition := controller.Transition(context.Background(), ModeServingReadOnly, 0)
		transitionDone <- errTransition
	}()

	select {
	case <-prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("read-only preparation did not start")
	}
	if _, admitted := controller.AdmitProxy(); admitted {
		t.Fatal("standby admitted traffic before read-only preparation completed")
	}
	close(allowPrepare)
	select {
	case errTransition := <-transitionDone:
		if errTransition != nil {
			t.Fatalf("standby -> serving-readonly error = %v", errTransition)
		}
	case <-time.After(time.Second):
		t.Fatal("read-only transition deadlocked")
	}
	if got := controller.Status().Mode; got != ModeServingReadOnly {
		t.Fatalf("mode after read-only preparation = %s", got)
	}
}

func TestControllerDrainingReturnsRetryableStateAndAllowsRollback(t *testing.T) {
	controller := NewController(ModeActive)
	controller.SetHooks(Hooks{
		ResumeActive: func(context.Context) (Components, error) { return Components{WriterLeaseHeld: true}, nil },
		Deactivate:   func(context.Context) (Components, error) { return Components{}, nil },
	})
	done, admitted := controller.AdmitProxy()
	if !admitted {
		t.Fatal("active rejected proxy traffic")
	}
	status, errDrain := controller.Transition(context.Background(), ModeDraining, 0)
	if errDrain != nil {
		t.Fatalf("active -> draining error = %v", errDrain)
	}

	if _, errStandby := controller.Transition(context.Background(), ModeStandby, status.Generation); !errors.Is(errStandby, ErrActiveRequests) {
		t.Fatalf("draining -> standby error = %v, want active requests", errStandby)
	}
	if status, errResume := controller.Transition(context.Background(), ModeActive, 0); errResume != nil || status.Mode != ModeActive {
		t.Fatalf("draining -> active rollback = %+v, %v", status, errResume)
	}
	done()
	status, errDrain = controller.Transition(context.Background(), ModeDraining, 0)
	if errDrain != nil {
		t.Fatalf("active -> draining after rollback error = %v", errDrain)
	}
	if _, errStandby := controller.Transition(context.Background(), ModeStandby, 0); errStandby != nil {
		t.Fatalf("draining -> standby after completion error = %v", errStandby)
	}
}

func TestControllerServingReadOnlyClosesAdmissionBeforeStandby(t *testing.T) {
	controller := NewController(ModeServingReadOnly)
	done, admitted := controller.AdmitProxy()
	if !admitted {
		t.Fatal("serving-readonly rejected proxy traffic")
	}
	status, errStandby := controller.Transition(context.Background(), ModeStandby, 0)
	if !errors.Is(errStandby, ErrActiveRequests) {
		t.Fatalf("serving-readonly -> standby error = %v, want active requests", errStandby)
	}
	if status.AcceptingNew {
		t.Fatal("serving-readonly kept admission open while requests drained")
	}
	if _, admitted := controller.AdmitProxy(); admitted {
		t.Fatal("serving-readonly admitted a request while standby was pending")
	}
	done()
	if _, errStandby := controller.Transition(context.Background(), ModeStandby, 0); errStandby != nil {
		t.Fatalf("serving-readonly -> standby after completion error = %v", errStandby)
	}
}

func TestControllerServingReadOnlyDrainsBeforeActivation(t *testing.T) {
	controller := NewController(ModeServingReadOnly)
	activated := 0
	controller.SetHooks(Hooks{Activate: func(context.Context) (Components, error) {
		activated++
		return Components{WriterLeaseHeld: true}, nil
	}})
	done, admitted := controller.AdmitProxy()
	if !admitted {
		t.Fatal("serving-readonly rejected initial request")
	}
	status, errActivate := controller.Transition(context.Background(), ModeActive, 0)
	if !errors.Is(errActivate, ErrActiveRequests) || status.AcceptingNew || activated != 0 {
		t.Fatalf("first activation = %+v, %v, hook calls=%d", status, errActivate, activated)
	}
	done()
	status, errActivate = controller.Transition(context.Background(), ModeActive, 0)
	if errActivate != nil || status.Mode != ModeActive || !status.AcceptingNew || activated != 1 {
		t.Fatalf("activation after drain = %+v, %v, hook calls=%d", status, errActivate, activated)
	}
}

func TestControllerTransitionLockHonorsContext(t *testing.T) {
	controller := NewController(ModeStandby)
	prepareStarted := make(chan struct{})
	allowPrepare := make(chan struct{})
	controller.SetHooks(Hooks{PrepareReadOnly: func(context.Context) error {
		close(prepareStarted)
		<-allowPrepare
		return nil
	}})
	firstDone := make(chan struct{})
	go func() {
		_, _ = controller.Transition(context.Background(), ModeServingReadOnly, 0)
		close(firstDone)
	}()
	<-prepareStarted
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, errTransition := controller.Transition(ctx, ModeServingReadOnly, 0); !errors.Is(errTransition, context.DeadlineExceeded) {
		t.Fatalf("queued transition error = %v, want deadline", errTransition)
	}
	close(allowPrepare)
	<-firstDone
}

func TestControllerCanceledTransitionDoesNotCommit(t *testing.T) {
	controller := NewController(ModeActive)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, errTransition := controller.Transition(ctx, ModeDraining, 0); !errors.Is(errTransition, context.Canceled) {
		t.Fatalf("canceled transition error = %v, want canceled", errTransition)
	}
	status := controller.Status()
	if status.Mode != ModeActive || !status.AcceptingNew {
		t.Fatalf("canceled transition changed state: %+v", status)
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
	if !controller.Status().AcceptingNew {
		t.Fatal("activation failure did not restore read-only admission")
	}
}

func TestControllerPublishesActualComponentsAfterResumeFailure(t *testing.T) {
	controller := NewController(ModeActive)
	controller.SetComponents(Components{
		CredentialWriter: true,
		WriterLeaseHeld:  true,
		AutoRefresh:      true,
		IPv6Enabled:      true,
		PluginRuntime:    true,
	})
	controller.SetHooks(Hooks{ResumeActive: func(context.Context) (Components, error) {
		return Components{}, errors.New("restore failed")
	}})
	if _, errDrain := controller.Transition(context.Background(), ModeDraining, 0); errDrain != nil {
		t.Fatal(errDrain)
	}
	status, errResume := controller.Transition(context.Background(), ModeActive, 0)
	if errResume == nil || status.Mode != ModeDraining {
		t.Fatalf("resume failure = %+v, %v", status, errResume)
	}
	if status.CredentialWriter || status.WriterLeaseHeld || status.AutoRefresh || status.IPv6Enabled || status.PluginRuntime {
		t.Fatalf("resume failure retained stale components: %+v", status)
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
