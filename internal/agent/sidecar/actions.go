package sidecar

// Action runs a command's handle_action, or for control_device with no such command (or
// params' "enabled" false) the device's family control (internal/agent/actions). It returns
// {success, error, input_required?, message}: N:'s device-control result, and what N: speaks.
func (s *Sidecar) Action(params map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := s.roundTrip("action", params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Callback runs a command's @callback and returns {success, error, context_data?}: the body
// N: posts to /callbacks/{job}/result.
func (s *Sidecar) Callback(params map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := s.roundTrip("callback", params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Action is Sidecar.Action on the sidecar in service.
func (l *Live) Action(params map[string]any) (map[string]any, error) {
	if sc := l.Current(); sc != nil {
		return sc.Action(params)
	}
	return nil, ErrNoPackages
}

// Callback is Sidecar.Callback on the sidecar in service.
func (l *Live) Callback(params map[string]any) (map[string]any, error) {
	if sc := l.Current(); sc != nil {
		return sc.Callback(params)
	}
	return nil, ErrNoPackages
}
