import json
import os
from pathlib import Path
import subprocess
import unittest


class AnalyticsDeploymentTest(unittest.TestCase):
    def test_worker_can_reach_google_without_publishing_a_port(self):
        root = Path(__file__).resolve().parents[2]
        result = subprocess.run(
            ["docker", "compose", "--env-file", ".env.example", "--profile", "analytics", "config", "--format", "json"],
            cwd=root,
            env={"PATH": os.environ["PATH"], "HOME": os.environ["HOME"]},
            capture_output=True,
            text=True,
            timeout=30,
            check=True,
        )
        config = json.loads(result.stdout)
        worker = config["services"]["analytics-worker"]
        self.assertTrue(any(not config["networks"][name].get("internal", False) for name in worker["networks"]))
        self.assertIn("backend", worker["networks"])
        self.assertFalse(worker.get("ports"))
        self.assertTrue(worker["read_only"])
        self.assertEqual(worker["command"], ["analytics-sync"])
        self.assertTrue(any(volume["target"] == "/analytics-runtime" for volume in worker["volumes"]))
        self.assertIn("GA4_SERVICE_ACCOUNT_JSON_BASE64", worker["environment"])
        self.assertNotIn("GA4_SERVICE_ACCOUNT_JSON_BASE64", config["services"]["writer"]["environment"])


if __name__ == "__main__":
    unittest.main()
