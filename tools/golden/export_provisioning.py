"""Export the provisioning API goldens for internal/agent/provisioning (go test reads
testdata/models_golden.json).

Needs only pydantic and a jarvis-node-setup checkout (its provisioning/models.py):

    uv run --with pydantic python tools/golden/export_provisioning.py \
        ~/jarvis/jarvis-node-setup > internal/agent/provisioning/testdata/models_golden.json

Each case is a model instance as FastAPI would answer it (model_dump_json): the Go test builds
the same value and compares bytes, so field names, order and null/default behaviour must match.
"""
import importlib.util
import json
import sys
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "models", Path(sys.argv[1]) / "provisioning" / "models.py")
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

S = m.ProvisioningState
E = m.ProvisioningErrorCode
cases = {
    "info": m.NodeInfo(node_id="jarvis-a1b2c3d4", firmware_version="1.0.0", hardware="pi-zero-w",
                       mac_address="b8:27:eb:a1:b2:c3", capabilities=["voice", "speaker"],
                       state=S.AP_MODE),
    "info_previously_provisioned": m.NodeInfo(
        node_id="jarvis-a1b2c3d4", firmware_version="1.0.0", hardware="raspberry-pi",
        mac_address="b8:27:eb:a1:b2:c3", capabilities=[], state=S.ERROR,
        previously_provisioned=True),
    "scan": m.ScanNetworksResponse(networks=[
        m.NetworkInfo(ssid="HomeNetwork", signal_strength=-45, security="WPA2"),
        m.NetworkInfo(ssid="Café <&>", signal_strength=-80, security="OPEN"),
    ]),
    "scan_empty": m.ScanNetworksResponse(),
    "provision_accepted": m.ProvisionResponse(
        success=True, message="Credentials received. Attempting connection..."),
    "provision_busy": m.ProvisionResponse(success=False, message="Provisioning already in progress"),
    "status_idle": m.ProvisionStatus(state=S.AP_MODE, message="Waiting for mobile app connection..."),
    "status_connecting": m.ProvisionStatus(state=S.CONNECTING, message="Connecting to HomeNetwork...",
                                           progress_percent=35),
    "status_registration_failed": m.ProvisionStatus(
        state=S.ERROR,
        message="Provisioning failed — reconnect to the node's setup network and try again",
        progress_percent=70,
        error="Command center rejected registration (HTTP 401): Invalid or expired provisioning token",
        error_code=E.REGISTRATION_FAILED, registration_status=401, retryable=True,
        hotspot_restored=True),
    "status_internal": m.ProvisionStatus(state=S.ERROR, message="Provisioning failed",
                                         progress_percent=10, error="boom",
                                         error_code=E.INTERNAL_ERROR),
    "status_provisioned": m.ProvisionStatus(
        state=S.PROVISIONED,
        message="Provisioning complete! You can now start the main Jarvis service.",
        progress_percent=100),
    "k2_ok": m.K2ProvisionResponse(success=True, node_id="0b7c4e1e-1111-2222-3333-444455556666",
                                   kid="k2-2026-01"),
    "k2_refused": m.K2ProvisionResponse(
        success=False, error="Node is already provisioned. Re-enter pairing mode to update K2."),
    "reset_ok": m.FactoryResetResponse(success=True, cleared=["provisioning_marker", "k2_key"]),
    "reset_failed": m.FactoryResetResponse(success=False, error="permission denied"),
}
out = {
    "states": [s.value for s in S],
    "error_codes": [e.value for e in E],
    "cases": {k: v.model_dump_json() for k, v in cases.items()},
}
json.dump(out, sys.stdout, indent=2, ensure_ascii=False)
print()
