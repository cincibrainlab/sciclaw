from pathlib import Path
import subprocess
import tempfile
import unittest


SKILLS = (
    "gitea-create-change-request",
    "gitea-move-work-item",
    "gitea-move-file-phase-0-to-1",
)


class EntrypointTest(unittest.TestCase):
    def test_preserves_non_directory_skill_entry_without_blocking_startup(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "source" / SKILLS[0]).mkdir(parents=True)
            (root / "source" / SKILLS[0] / "SKILL.md").write_text("packaged")
            (root / "source" / SKILLS[1]).mkdir()
            (root / "source" / SKILLS[1] / "SKILL.md").write_text("packaged")
            (root / "workspace").mkdir()
            entry = root / "workspace" / SKILLS[0]
            entry.write_text("custom")
            dangling = root / "workspace" / SKILLS[1]
            dangling.symlink_to(root / "missing")
            script = Path(__file__).with_name("docker-entrypoint.sh").read_text()
            script = script.replace("/opt/sciclaw/skills", str(root / "source"))
            script = script.replace("/root/sciclaw/skills", str(root / "workspace"))
            script = script.replace('exec sciclaw "$@"', 'exec "$@"')
            result = subprocess.run(["sh", "-c", script, "entrypoint", "true"], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(SKILLS[0], result.stderr)
            self.assertEqual(entry.read_text(), "custom")
            self.assertTrue(dangling.is_symlink())

    def test_warns_when_stale_workspace_skill_has_no_packaged_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            skill = root / "workspace" / SKILLS[0]
            skill.mkdir(parents=True)
            (skill / "SKILL.md").write_text("old guidance")
            script = Path(__file__).with_name("docker-entrypoint.sh").read_text()
            script = script.replace("/opt/sciclaw/skills", str(root / "source"))
            script = script.replace("/root/sciclaw/skills", str(root / "workspace"))
            script = script.replace('exec sciclaw "$@"', 'exec "$@"')
            result = subprocess.run(["sh", "-c", script, "entrypoint", "true"], check=True, capture_output=True, text=True)
            self.assertIn(SKILLS[0], result.stderr)
            self.assertEqual((skill / "SKILL.md").read_text(), "old guidance")

    def test_absent_skills_do_not_create_workspace(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            script = Path(__file__).with_name("docker-entrypoint.sh").read_text()
            script = script.replace("/opt/sciclaw/skills", str(root / "source"))
            script = script.replace("/root/sciclaw/skills", str(root / "workspace"))
            script = script.replace('exec sciclaw "$@"', 'exec "$@"')
            subprocess.run(["sh", "-c", script, "entrypoint", "true"], check=True)
            self.assertFalse((root / "workspace").exists())


if __name__ == "__main__":
    unittest.main()
