-- Inbound transfer (loop #448, docs/cc/phone-sip.md §3.4 and §4 step 4): the receptionist may
-- put a caller through, with a blind SIP REFER, to one of the household's configured transfer
-- targets (phone.inbound.transfer_targets). outcome stays what it says about the message; these
-- say what became of the one transfer a call may try. transfer_label is the target's name as
-- the household configured it (its number is not copied here); transfer_result transferred (the
-- PBX reported the target taking the call, and Jarvis hung up its leg) or failed;
-- transfer_detail is why it failed, in plain words. NULL: no transfer was tried.
-- +goose Up
ALTER TABLE cc_phone_inbound_calls ADD COLUMN transfer_label TEXT;
ALTER TABLE cc_phone_inbound_calls ADD COLUMN transfer_result TEXT CHECK (transfer_result IS NULL OR transfer_result IN ('transferred', 'failed'));
ALTER TABLE cc_phone_inbound_calls ADD COLUMN transfer_detail TEXT;

-- +goose Down
ALTER TABLE cc_phone_inbound_calls DROP COLUMN transfer_detail;
ALTER TABLE cc_phone_inbound_calls DROP COLUMN transfer_result;
ALTER TABLE cc_phone_inbound_calls DROP COLUMN transfer_label;
