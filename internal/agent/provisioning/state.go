package provisioning

import "sync"

// Messages the app shows or matches (it treats a refused provision whose message says
// "in progress" as "the node already has the credentials").
const (
	msgWaiting        = "Waiting for mobile app connection..."
	msgAccepted       = "Credentials received. Attempting connection..."
	msgBusy           = "Provisioning already in progress"
	msgFailed         = "Provisioning failed"
	msgFailedRestored = "Provisioning failed — reconnect to the node's setup network and try again"
	msgRestoring      = "Provisioning failed — restoring setup hotspot..."
	msgProvisioned    = "Provisioning complete! You can now start the main Jarvis service."
	msgK2Provisioned  = "Node is already provisioned. Re-enter pairing mode to update K2."
)

// StateMachine tracks one provisioning attempt's progress (state_machine.py). Safe for
// concurrent use.
type StateMachine struct {
	mu sync.Mutex
	s  Status
}

// NewStateMachine starts in AP_MODE, waiting for the app.
func NewStateMachine() *StateMachine {
	return &StateMachine{s: Status{State: StateAPMode, Message: msgWaiting}}
}

// State is the current state.
func (m *StateMachine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.s.State
}

// Busy says whether an attempt is in flight (CONNECTING or REGISTERING).
func (m *StateMachine) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.s.State == StateConnecting || m.s.State == StateRegistering
}

// Transition moves to state with a message; progress < 0 keeps the current one. Any state but
// ERROR clears the error details.
func (m *StateMachine) Transition(state State, message string, progress int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s.State, m.s.Message = state, message
	if progress >= 0 {
		m.s.ProgressPercent = min(progress, 100)
	}
	if state != StateError {
		m.clearErrorLocked()
	}
}

// Failure is what SetError records.
type Failure struct {
	Error              string
	Code               ErrorCode
	RegistrationStatus *int
	Retryable          bool
	HotspotRestored    bool
	Message            string // "" means "Provisioning failed"
}

// SetError moves to ERROR (progress is kept, as in Python).
func (m *StateMachine) SetError(f Failure) {
	if f.Code == "" {
		f.Code = ErrInternal
	}
	if f.Message == "" {
		f.Message = msgFailed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s.State, m.s.Message = StateError, f.Message
	m.s.Error, m.s.ErrorCode = &f.Error, &f.Code
	m.s.RegistrationStatus = f.RegistrationStatus
	m.s.Retryable, m.s.HotspotRestored = f.Retryable, f.HotspotRestored
}

// Status is GET /api/v1/status's body.
func (m *StateMachine) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.s
}

// Reset goes back to AP_MODE with no progress or error.
func (m *StateMachine) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s.State, m.s.Message, m.s.ProgressPercent = StateAPMode, msgWaiting, 0
	m.clearErrorLocked()
}

func (m *StateMachine) clearErrorLocked() {
	m.s.Error, m.s.ErrorCode, m.s.RegistrationStatus = nil, nil, nil
	m.s.Retryable, m.s.HotspotRestored = false, false
}
