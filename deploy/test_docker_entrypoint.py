from pathlib import Path
import subprocess
import tempfile
import unittest


SKILLS = (
    "gitea-create-change-request",
    "gitea-move-work-item",
    "gitea-move-file-phase-0-to-1",
)
LEGACY = "move-task-phase-0-to-1"


class EntrypointTest(unittest.TestCase):
    def test_packaged_skills_seed_without_overwriting_workspace(self):
        for supplied in ((), SKILLS[:1], SKILLS):
            with self.subTest(supplied=supplied), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                source = root / "source"
                workspace = root / "workspace"
                source.mkdir()
                workspace.mkdir()
                for skill in (SKILLS[0], SKILLS[2], LEGACY, "unrelated"):
                    (workspace / skill).mkdir()
                    (workspace / skill / "SKILL.md").write_text("existing")
                (workspace / SKILLS[1]).symlink_to(root / "missing")
                for skill in supplied:
                    (source / skill).mkdir()
                    (source / skill / "SKILL.md").write_text("packaged")
                script = Path(__file__).with_name("docker-entrypoint.sh").read_text()
                script = script.replace("/opt/sciclaw/skills", str(source))
                script = script.replace("/root/sciclaw/skills", str(workspace))
                script = script.replace('exec sciclaw "$@"', 'exec "$@"')
                subprocess.run(["sh", "-c", script, "entrypoint", "true"], check=True)
                for skill in (SKILLS[0], SKILLS[2]):
                    self.assertEqual((workspace / skill / "SKILL.md").read_text(), "existing")
                self.assertTrue((workspace / SKILLS[1]).is_symlink())
                self.assertEqual((workspace / LEGACY / "SKILL.md").read_text(), "existing")
                self.assertEqual((workspace / "unrelated" / "SKILL.md").read_text(), "existing")

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
