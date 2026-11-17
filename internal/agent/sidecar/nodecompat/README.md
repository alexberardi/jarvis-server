# nodecompat: the Python node's modules, for Pantry packages

Pantry packages were written for the Python node (jarvis-node-setup) and import its modules:
`services.secret_service`, `utils.config_service`, `device_families.domains`,
`commands.custom_commands.<x>`, and so on. The sidecar gives them the same modules, so a
package works on the Go agent exactly as it did on the node
([decision](../../../../docs/decisions/2026-11-16-node-modules-for-packages.md)).

The sidecar writes this tree beside its script, puts it on `sys.path` after the package
roots (a package's own code wins), and joins it to the shared top-level packages
(`device_families` is one package over every root's and this one's).

Two kinds of module:

- **The node's own code, copied verbatim** from jarvis-node-setup at the commit in each file's
  header (the one `packages.NodeCommandsRequirement` pins): `device_families/domains/`,
  `constants/`, `utils/timezone_util.py`, `utils/date_util.py`, `utils/ha_training_data.py`,
  `services/node_llm_client.py`, `services/proposal_client.py`. Don't edit them here; the
  Python node is frozen, and a change belongs there first.
- **The node's I/O modules, over the Go agent** (`jarvis_sidecar._NODE_IO_MODULES`, registered
  by name so they also win over a Pi's jarvis-node-setup, whose copies read the Python
  node's database): secrets and auth flags in the agent's store; `Config` and service
  discovery from `node.info` (jarvisd's `GET /api/v0/node/info`); `RestClient` as
  `cc.request`, which the agent signs (Python never holds the node key, so `Config`'s
  `api_key` reads as unset); the direct devices from the agent's registry with this
  sidecar's device families; `text_to_speech` as `audio.speak`.

`TestNodeModulesForPackages` calls every one the way the 2026-11-16 inventory of the 29
published packages does. When a package imports a node module not here, add it the same way
(copy pure code; write I/O over `jarvis_node_compat`) with a case in that test.
