-- The L2 lane's trunk to the fake carrier and its outbound route, written straight into
-- FreePBX 17's database (GraphQL cannot create outbound routes, and bulkimport has no trunks
-- or routes). Tables and columns as FreePBX's core module (release/17.0) keeps them: trunks and
-- pjsip (functions.inc.php core_trunks_add, the PJSip driver's trunk keywords) and
-- outbound_routes, outbound_route_patterns, outbound_route_trunks, outbound_route_sequence
-- (Core.class.php / functions.inc.php core_routing_*). Not checked against a live database by
-- the agent that wrote it: the first `scripts/freepbx-lane build` is its test.
--
-- The carrier is the host's loopback as qemu's user network shows it to the VM (10.0.2.2), on
-- the L1 lane's carrier port; it is never a real carrier. Same numbers as L1's dialplan:
-- NXXNXXXXXX and 1NXXNXXXXXX go out to it.

SET @trunk := (SELECT COALESCE(MAX(trunkid), 0) + 1 FROM trunks);
INSERT INTO trunks (trunkid, name, tech, outcid, keepcid, maxchans, failscript, dialoutprefix, channelid, usercontext, provider, disabled, `continue`)
VALUES (@trunk, 'carrier', 'pjsip', '', 'off', '', '', '', 'carrier', '', '', 'off', 'off');

INSERT INTO pjsip (id, keyword, data, flags) VALUES
  (@trunk, 'trunk_name', 'carrier', 0),
  (@trunk, 'sip_server', '10.0.2.2', 0),
  (@trunk, 'sip_server_port', '5072', 0),
  (@trunk, 'transport', '0.0.0.0-udp', 0),
  (@trunk, 'authentication', 'inbound', 0),
  (@trunk, 'registration', 'none', 0),
  (@trunk, 'username', 'carrier', 0),
  (@trunk, 'secret', 'lane-carrier', 0),
  (@trunk, 'context', 'from-pstn', 0),
  (@trunk, 'codecs', 'ulaw,alaw', 0),
  (@trunk, 'dtmfmode', 'rfc4733', 0),
  (@trunk, 'direct_media', 'no', 0),
  (@trunk, 'rtp_symmetric', 'yes', 0),
  (@trunk, 'rewrite_contact', 'yes', 0),
  (@trunk, 'force_rport', 'yes', 0),
  (@trunk, 'sendrpid', 'pai', 0),
  (@trunk, 'trust_rpid', 'yes', 0),
  (@trunk, 'qualify_frequency', '0', 0);

INSERT INTO outbound_routes (name, outcid, outcid_mode, password, emergency_route, intracompany_route, mohclass, time_group_id, dest)
VALUES ('carrier', '', '', '', '', '', 'default', NULL, '');
SET @route := LAST_INSERT_ID();

INSERT INTO outbound_route_patterns (route_id, match_pattern_prefix, match_pattern_pass, match_cid, prepend_digits) VALUES
  (@route, '', 'NXXNXXXXXX', '', ''),
  (@route, '', '1NXXNXXXXXX', '', '');

INSERT INTO outbound_route_trunks (route_id, trunk_id, seq) VALUES (@route, @trunk, 0);

SET @seq := (SELECT COALESCE(MAX(seq), -1) + 1 FROM outbound_route_sequence);
INSERT INTO outbound_route_sequence (route_id, seq) VALUES (@route, @seq);
