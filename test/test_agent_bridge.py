"""Bridge behavior checks; run in CI, not on the deployment host."""
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "internal/agentkit/schedulerse-delivery/scripts/bridge.py"
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

    def test_local_pickup_retries_lost_response_before_executing(self):
        self.config.update(transport="scheduler", scheduler_url="http://schedulerse:8789", allow_insecure_network=True,
                           api_token="private-api", consumer_id="stable-random-consumer-identity-123")
        self.event["task"]["run_at_unix"] = 1792566000
        def uncertain(config, method, path, payload=None):
            if method == "GET":
                return [self.event]
            with bridge.database(self.state) as other:
                self.assertEqual(other.execute("SELECT status FROM receipts").fetchone()[0], "awaiting")
            raise OSError("server accepted pickup but response was lost")
        with patch.object(bridge, "request_json", side_effect=uncertain):
            with self.assertRaises(OSError):
                bridge.intake_local(self.db, self.config)
        with patch.object(bridge.subprocess, "run") as invoke:
            bridge.dispatch(self.db, self.state, self.config)
            invoke.assert_not_called()
        # Acknowledged occurrences are no longer listed, but awaiting local receipts retry.
        with patch.object(bridge, "request_json", side_effect=[[], None]) as remote:
            bridge.intake_local(self.db, self.config)
            self.assertEqual(remote.call_args_list[-1].args[3]["consumer_id"], self.config["consumer_id"])
        self.assertEqual(self.db.execute("SELECT status FROM receipts").fetchone()[0], "received")
        with patch.object(bridge.subprocess, "run", return_value=SimpleNamespace(returncode=0)) as invoke:
            bridge.dispatch(self.db, self.state, self.config)
            bridge.dispatch(self.db, self.state, self.config)
            self.assertEqual(invoke.call_count, 1)

    def test_local_competing_consumer_skips_agent_invocation(self):
        self.config.update(transport="scheduler", consumer_id="stable-random-consumer-identity-123")
        self.event["task"]["run_at_unix"] = 1792566000
        conflict = bridge.urllib.error.HTTPError("http://schedulerse:8789", 409, "Claimed elsewhere", {}, None)
        with patch.object(bridge, "request_json", side_effect=[[self.event], conflict]):
            bridge.intake_local(self.db, self.config)
        self.assertEqual(self.db.execute("SELECT status FROM receipts").fetchone()[0], "skipped")
        with patch.object(bridge.subprocess, "run") as invoke:
            bridge.dispatch(self.db, self.state, self.config)
            invoke.assert_not_called()

    def test_local_origin_accepts_docker_service_dns(self):
        with self.assertRaises(ValueError):
            bridge.validate_scheduler_origin("http://schedulerse:8789")
        bridge.validate_scheduler_origin("http://schedulerse:8789", True)
        for local in ("http://localhost:8789", "http://127.0.0.1:8789", "http://[::1]:8789"):
            bridge.validate_scheduler_origin(local)
        for invalid in ("http://secret@schedulerse:8789", "http://schedulerse:8789/?token=secret"):
            with self.assertRaises(ValueError):
                bridge.validate_scheduler_origin(invalid)

    def test_remote_plaintext_is_rejected_before_token_is_sent(self):
        config = {"transport": "scheduler", "scheduler_url": "http://remote.example:8789", "api_token": "private"}
        with patch.object(bridge.urllib.request, "build_opener") as opener:
            with self.assertRaises(ValueError):
                bridge.request_json(config, "GET", "/inbox")
            opener.assert_not_called()

    def test_local_setup_uses_packaged_address_and_configures_polling(self):
        token_file = self.state / "api-token"
        token_file.write_text("private-setup-token")
        args = SimpleNamespace(action="init-local", scheduler_url=None,
            token_file=str(token_file), workdir=str(self.state), conversation_id="expected-thread",
            codex_session=None, command_json=None)
        with patch.object(bridge, "packaged_service_url", return_value="https://192.0.2.5:9443"), \
             patch.object(bridge, "request_json", return_value={"delivery_mode": "poll"}) as remote:
            bridge.initialize(args, self.state)
        config = json.loads((self.state / "config.json").read_text())
        self.assertEqual(config["scheduler_url"], "https://192.0.2.5:9443")
        self.assertEqual(config["api_token"], "private-setup-token")
        self.assertEqual(remote.call_args.args[1:], ("PUT", "/connection", {"delivery_mode": "poll"}))
        self.assertFalse((self.state / "cloudflare-secrets.json").exists())
        # Retrying an initialized connection cannot silently change shared delivery settings.
        with patch.object(bridge, "request_json") as remote:
            with self.assertRaises(ValueError):
                bridge.initialize(args, self.state)
            remote.assert_not_called()

    def test_failed_connection_setup_does_not_save_half_initialized_config(self):
        token_file = self.state / "api-token"
        token_file.write_text("private-setup-token")
        args = SimpleNamespace(action="init-local", scheduler_url="http://schedulerse:8789", allow_insecure_network=True,
            token_file=str(token_file), workdir=str(self.state), conversation_id="expected-thread",
            codex_session=None, command_json=None)
        with patch.object(bridge, "request_json", side_effect=OSError("unreachable")):
            with self.assertRaises(OSError):
                bridge.initialize(args, self.state)
        self.assertFalse((self.state / "config.json").exists())

    def test_delivery_kit_reads_service_origin_from_adjacent_metadata(self):
        kit = self.state / "delivery-kit"
        scripts = kit / "scripts"
        scripts.mkdir(parents=True)
        metadata = kit / "connection.json"
        metadata.write_text(json.dumps({"service_url": "http://192.0.2.10:8789"}))
        with patch.object(bridge, "__file__", str(scripts / "bridge.py")):
            with self.assertRaises(ValueError):
                bridge.packaged_service_url()
            self.assertEqual(bridge.packaged_service_url(True), "http://192.0.2.10:8789")
            metadata.write_text(json.dumps({"service_url": "https://user:secret@example.com"}))
            with self.assertRaises(ValueError):
                bridge.packaged_service_url()


if __name__ == "__main__":
    unittest.main()
