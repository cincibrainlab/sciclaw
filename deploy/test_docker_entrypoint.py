from pathlib import Path
import subprocess
import tempfile
import unittest


class EntrypointTest(unittest.TestCase):
    def test_warns_and_preserves_existing_skills_without_seeding(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            workspace = root / "workspace"
            source = root / "source"
            existing = workspace / "gitea-create-change-request"
            existing.mkdir(parents=True)
            (existing / "SKILL.md").write_text("custom")
            (workspace / "move-task-phase-0-to-1").symlink_to(root / "missing")
            supplied = source / "gitea-move-work-item"
            supplied.mkdir(parents=True)
            (supplied / "SKILL.md").write_text("packaged")
            script = Path(__file__).with_name("docker-entrypoint.sh").read_text()
            script = script.replace("/root/sciclaw/skills", str(workspace))
            script = script.replace("/opt/sciclaw/skills", str(source))
            script = script.replace('exec sciclaw "$@"', 'exec "$@"')
            result = subprocess.run(["sh", "-c", script, "entrypoint", "printf", "%s\\n", "gateway"], check=True, capture_output=True, text=True)
            self.assertEqual(result.stdout, "gateway\n")
            self.assertIn("gitea-create-change-request", result.stderr)
            self.assertIn("move-task-phase-0-to-1", result.stderr)
            self.assertIn("review", result.stderr.lower())
            self.assertEqual((existing / "SKILL.md").read_text(), "custom")
            self.assertTrue((workspace / "move-task-phase-0-to-1").is_symlink())
            self.assertFalse((workspace / "gitea-move-work-item").exists())


if __name__ == "__main__":
    unittest.main()
