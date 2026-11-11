package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// A Forge test install's device protocol lasts the test (testRunWindow after it installed).
// Devices imported through it are tagged with the test's node and package (cc_devices
// test_node_id, test_package) and deleted once that node runs no test of that package: without
// the protocol nothing controls them (user 2026-11-10). A Reinstall is a newer request for the
// same package, so a fix round keeps them.

type testOwner struct{ nodeID, pkg string }

// testProtocols are the device protocols of the household's running test installs, by name;
// the newest install wins.
func (m *Module) testProtocols(ctx context.Context, q *sql.Tx, hh string) (map[string]testOwner, error) {
	rows, err := q.QueryContext(ctx, `SELECT node_id, package_name, results_json FROM cc_test_install_requests
		WHERE household_id = ? AND status = ? AND completed_at > ? ORDER BY completed_at`,
		hh, pkgCompleted, dbTime(m.now().Add(-testRunWindow)))
	if err != nil {
		return nil, fmt.Errorf("test install protocols: %w", err)
	}
	defer rows.Close()
	out := map[string]testOwner{}
	for rows.Next() {
		var node, pkg string
		var res sql.NullString
		if err := rows.Scan(&node, &pkg, &res); err != nil {
			return nil, err
		}
		var d struct {
			Protocols []string `json:"protocols"`
		}
		_ = json.Unmarshal([]byte(res.String), &d)
		for _, p := range d.Protocols {
			out[p] = testOwner{node, pkg}
		}
	}
	return out, rows.Err()
}

// sweepTestDevices deletes the devices of test installs that have ended, in household hh ("":
// every household).
func (m *Module) sweepTestDevices(ctx context.Context, hh string) error {
	rows, err := m.deps.DB.Write.QueryContext(ctx, `DELETE FROM cc_devices
		WHERE test_package IS NOT NULL AND (? = '' OR household_id = ?)
		AND NOT EXISTS (SELECT 1 FROM cc_test_install_requests t WHERE t.node_id = cc_devices.test_node_id
			AND t.package_name = cc_devices.test_package AND t.status = ? AND t.completed_at > ?)
		RETURNING household_id, entity_id, test_package`,
		hh, hh, pkgCompleted, dbTime(m.now().Add(-testRunWindow)))
	if err != nil {
		return fmt.Errorf("test install devices sweep: %w", err)
	}
	households := map[string]bool{}
	for rows.Next() {
		var h, entity, pkg string
		if err := rows.Scan(&h, &entity, &pkg); err != nil {
			rows.Close()
			return err
		}
		households[h] = true
		m.deps.Log.Info("cc: removed a test install's device", "household", h, "device", entity, "package", pkg)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for h := range households {
		m.invalidateDeviceCache(ctx, h)
	}
	return nil
}
