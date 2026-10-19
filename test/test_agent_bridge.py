"""Bridge behavior checks; run in CI, not on the deployment host."""
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "skills/schedulerse-agent/scripts/bridge.py"
spec = importlib.util.spec_from_file_location("agent_bridge", SOURCE)
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


class BridgeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.state = Path(self.temp.name)
        self.db = bridge.database(self.state)
        self.addCleanup(self.db.close)
        self.config = {"relay_url": "https://example.workers.dev", "poll_token": "private-poll",
                       "conversation_id": "expected-thread", "adapter": "command",
                       "command": ["configured-agent"], "workdir": str(self.state)}
        self.event = {"event": "schedule.due", "event_id": "task-one:2026-10-21T07:00:00Z",
                      "task": {"id": "task-one", "kind": "reminder",
                               "context": {"conversation_id": "expected-thread"}}}

    def test_receipt_is_durable_before_ack_and_deduplicated_after_uncertain_ack(self):
        def remote(config, method, path):
            if method == "GET":
                return [self.event]
            # Reopen independently: an uncommitted receipt would be invisible.
            with bridge.database(self.state) as other:
                self.assertEqual(other.execute("SELECT status FROM receipts").fetchone()[0], "received")
            raise OSError("lost acknowledgement response")
        with patch.object(bridge, "request_json", side_effect=remote):
            with self.assertRaises(OSError):
                bridge.intake(self.db, self.config)
        with patch.object(bridge, "request_json", side_effect=[[self.event], None]):
            bridge.intake(self.db, self.config)
        self.assertEqual(self.db.execute("SELECT count(*) FROM receipts").fetchone()[0], 1)

    def test_claim_precedes_agent_action_and_failed_actions_are_not_replayed(self):
        with patch.object(bridge, "request_json", side_effect=[[self.event], None]):
            bridge.intake(self.db, self.config)
        def invoke(command, **kwargs):
            self.assertEqual(command, ["configured-agent"])
            self.assertNotIn("shell", kwargs)
            self.assertEqual(json.loads(kwargs["input"]), self.event)
            with bridge.database(self.state) as other:
                self.assertEqual(other.execute("SELECT status FROM receipts").fetchone()[0], "running")
            return SimpleNamespace(returncode=1)
        with patch.object(bridge.subprocess, "run", side_effect=invoke) as call:
            bridge.dispatch(self.db, self.state, self.config)
            bridge.dispatch(self.db, self.state, self.config)
            self.assertEqual(call.call_count, 1)
        self.assertEqual(self.db.execute("SELECT status FROM receipts").fetchone()[0], "failed")

    def test_wrong_conversation_is_neither_invoked_nor_acknowledged(self):
        self.event["task"]["context"]["conversation_id"] = "another-thread"
        with patch.object(bridge, "request_json", return_value=[self.event]) as remote:
            with self.assertRaises(ValueError):
                bridge.intake(self.db, self.config)
            self.assertEqual(remote.call_count, 1)
        self.assertEqual(self.db.execute("SELECT count(*) FROM receipts").fetchone()[0], 0)

    def test_codex_uses_explicit_session_and_fixed_permissions(self):
        self.config.update(adapter="codex", codex_session="chosen-codex-session")
        self.event["task"]["context"]["command"] = "untrusted-command"
        with patch.object(bridge, "request_json", side_effect=[[self.event], None]):
            bridge.intake(self.db, self.config)
        with patch.object(bridge.subprocess, "run", return_value=SimpleNamespace(returncode=0)) as call:
            bridge.dispatch(self.db, self.state, self.config)
        argv = call.call_args.args[0]
        self.assertEqual(argv[:4], ["codex", "exec", "--sandbox", "read-only"])
        self.assertEqual(argv[-3:], ["resume", "chosen-codex-session", "-"])
        self.assertNotIn("--last", argv)
        self.assertNotIn("untrusted-command", argv)

    def test_relay_credentials_are_not_forwarded_to_redirects(self):
        self.assertIsNone(bridge.NoRedirect().redirect_request(None, None, 302, "", {}, "https://other.example"))
        for bad in ("http://example.com", "https://secret@example.com", "https://example.com/?token=x"):
            with self.assertRaises(ValueError):
                bridge.validate_origin(bad)


if __name__ == "__main__":
    unittest.main()
