from pathlib import Path
import os
import subprocess
import tempfile
import unittest


class EntrypointTest(unittest.TestCase):
    def test_gateway_receives_arguments_without_modifying_workspace(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            workspace = root / "workspace"
            workspace.mkdir()
            for skill in ("gitea-create-change-request", "move-task-phase-0-to-1", "unrelated"):
                (workspace / skill).mkdir()
                (workspace / skill / "SKILL.md").write_text("custom")
            binary = root / "sciclaw"
            binary.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
            binary.chmod(0o755)
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"]}
            script = root / "entrypoint.sh"
            original = Path(__file__).with_name("docker-entrypoint.sh").read_text()
            script.write_text(original.replace("/root/sciclaw/skills", str(workspace)).replace("/opt/sciclaw/skills", str(root / "source")))
            for _ in range(2):
                result = subprocess.run(["sh", str(script), "gateway", "--test"], env=env, check=True, capture_output=True, text=True)
                self.assertEqual(result.stdout, "gateway\n--test\n")
                for skill in ("gitea-create-change-request", "move-task-phase-0-to-1", "unrelated"):
                    self.assertEqual((workspace / skill / "SKILL.md").read_text(), "custom")


if __name__ == "__main__":
    unittest.main()
